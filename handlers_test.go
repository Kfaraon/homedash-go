package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestRoutingExactPaths(t *testing.T) {
	_, h := newTestRouter(t, defaultGroups())

	tests := []struct {
		path string
		want int
	}{
		{"/", http.StatusOK},
		{"/admin", http.StatusOK},
		{"/admin/", http.StatusMovedPermanently},
		{"/foo", http.StatusNotFound},
		{"/api/nope", http.StatusNotFound},
		{"/admin/foo", http.StatusNotFound},
		{"/static/home.css", http.StatusOK},
	}

	for _, tc := range tests {
		rr := doJSON(t, h, http.MethodGet, tc.path, "", nil)
		if rr.Code != tc.want {
			t.Errorf("%s: got %d, want %d", tc.path, rr.Code, tc.want)
		}
	}
}

func TestHealthDegradedReturns503(t *testing.T) {
	app, cfg := newTestApp(t, defaultGroups())
	h := app.buildRouter()

	if err := os.WriteFile(cfg, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	rr := doJSON(t, h, http.MethodGet, "/health", "", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("degraded health: got %d, want 503", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "degraded") {
		t.Fatalf("unexpected body: %s", rr.Body.String())
	}
}

func TestHealthOK(t *testing.T) {
	_, h := newTestRouter(t, defaultGroups())
	rr := doJSON(t, h, http.MethodGet, "/health", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("health: got %d, want 200", rr.Code)
	}
}

func TestAdminRejectsCrossOrigin(t *testing.T) {
	_, h := newTestRouter(t, defaultGroups())

	rr := doJSON(t, h, http.MethodPost, "/api/admin/group", `{"name":"X"}`, map[string]string{
		"Origin": "http://evil.example",
	})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-origin admin: got %d, want 403", rr.Code)
	}

	same := doJSON(t, h, http.MethodPost, "/api/admin/group", `{"name":"X"}`, map[string]string{})
	if same.Code != http.StatusOK {
		t.Fatalf("same-origin admin: got %d (%s)", same.Code, same.Body.String())
	}
}

func TestSecurityHeaders(t *testing.T) {
	_, h := newTestRouter(t, defaultGroups())
	rr := doJSON(t, h, http.MethodGet, "/admin", "", nil)
	if rr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing X-Content-Type-Options")
	}
	if rr.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatal("missing X-Frame-Options for admin")
	}
}

func TestStatusEndpoint(t *testing.T) {
	_, h := newTestRouter(t, defaultGroups())
	rr := doJSON(t, h, http.MethodGet, "/api/status", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("unexpected content type %q", ct)
	}
}

func TestIsPublicIP(t *testing.T) {
	public := []string{"8.8.8.8", "1.1.1.1", "37.140.93.185", "2606:4700:4700::1111"}
	for _, s := range public {
		if !isPublicIP(net.ParseIP(s)) {
			t.Errorf("%s should be public", s)
		}
	}
	private := []string{
		"127.0.0.1", "10.0.0.1", "192.168.1.1", "172.16.5.5", "172.31.255.1",
		"169.254.1.1", "100.64.0.1", "100.127.255.254", "0.0.0.0", "224.0.0.1",
		"198.18.0.1", "198.51.100.7", "192.0.0.1", "192.0.2.1", "203.0.113.1",
		"fe80::1", "::1", "fc00::1", "fd12:3456::1",
	}
	for _, s := range private {
		if isPublicIP(net.ParseIP(s)) {
			t.Errorf("%s should NOT be public", s)
		}
	}
	if isPublicIP(nil) {
		t.Error("nil should not be public")
	}
}

// Проверяем, что провайдер, отдающий приватный адрес, пропускается,
// а следующий публичный — принимается.
func TestFetchPublicIPSkipsNonPublic(t *testing.T) {
	initHTTPTransports()

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("192.168.1.1\n"))
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("37.140.93.185\n"))
	}))
	defer good.Close()

	app := &App{IPProviders: []string{bad.URL, good.URL}}
	ip, ipType, provider := app.fetchPublicIP(context.Background())
	if ip != "37.140.93.185" {
		t.Fatalf("ip = %q, want the public provider's address", ip)
	}
	if ipType != "ipv4" {
		t.Fatalf("ipType = %q, want ipv4", ipType)
	}
	if provider == "" {
		t.Fatal("provider must be set")
	}
}

// Если все провайдеры отдают мусор — возвращается пустой результат.
func TestFetchPublicIPAllInvalid(t *testing.T) {
	initHTTPTransports()

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("not-an-ip\n"))
	}))
	defer bad.Close()

	app := &App{IPProviders: []string{bad.URL}}
	if ip, _, _ := app.fetchPublicIP(context.Background()); ip != "" {
		t.Fatalf("ip = %q, want empty", ip)
	}
}
