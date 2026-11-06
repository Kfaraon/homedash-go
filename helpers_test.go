package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig writes a valid config file.
func writeConfig(t *testing.T, path string, groups []Group) {
	t.Helper()
	data, err := json.Marshal(struct {
		Groups []Group `json:"groups"`
	}{Groups: groups})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// newTestApp builds an App backed by a temporary config file and isolates env.
func newTestApp(t *testing.T, groups []Group) (*App, string) {
	t.Helper()

	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	writeConfig(t, cfg, groups)

	t.Setenv("CONFIG_FILE", cfg)
	t.Setenv("ICONS_CONFIG_PATH", "data/icons.json")
	t.Setenv("PORT", "5000")
	t.Setenv("MAX_WORKERS", "4")
	t.Setenv("IDLE_TIMEOUT", "1m")
	t.Setenv("LAZY_CHECK_INTERVAL", "1m")
	t.Setenv("CHECK_TIMEOUT", "1s")
	t.Setenv("PING_TIMEOUT", "1s")
	t.Setenv("IP_CACHE_TTL", "1m")
	t.Setenv("ALLOWED_ORIGINS", "")

	adminRateLimiter = newRateLimiter(1000, 1000)

	app, err := NewApp()
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}

	t.Cleanup(func() {
		select {
		case <-app.Done:
		default:
			close(app.Done)
		}
	})

	return app, cfg
}

// defaultGroups returns a valid group with a single service.
func defaultGroups() []Group {
	return []Group{{
		Name: "Test",
		Services: []Service{
			{Name: "Local", URL: "http://127.0.0.1:1", VerifySSL: false},
		},
	}}
}

// newTestRouter returns an App and its router.
func newTestRouter(t *testing.T, groups []Group) (*App, http.Handler) {
	t.Helper()
	app, _ := newTestApp(t, groups)
	return app, app.buildRouter()
}

// doJSON performs a request against the router.
func doJSON(t *testing.T, h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}
