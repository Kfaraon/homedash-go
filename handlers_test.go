package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// setupAdminTest creates a test server with admin endpoints
func setupAdminTest(t *testing.T) (*httptest.Server, func()) {
	t.Helper()

	// Set API key for tests
	oldKey := adminAPIKey
	adminAPIKey = "test-secret-key"

	// Temporary config
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "config.json")
	initialConfig := map[string]any{
		"groups": []map[string]any{
			{
				"name":     "TestGroup",
				"services": []map[string]any{},
			},
		},
	}
	writeJSON(t, cfg, initialConfig)

	oldConfigFile := configFile
	configFile = cfg

	// Load groups
	groups, err := loadGroups(cfg)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	state.groupsMu.Lock()
	state.groups = groups
	state.groupsMu.Unlock()

	// Create test server
	mux := http.NewServeMux()
	adminMux := http.NewServeMux()
	adminMux.HandleFunc("/api/admin/groups", apiAdminGroups)
	adminMux.HandleFunc("/api/admin/group", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			apiAdminGroups(w, r)
		case http.MethodPost:
			apiAdminAddGroup(w, r)
		case http.MethodDelete:
			apiAdminDeleteGroup(w, r)
		case http.MethodPut:
			apiAdminRenameGroup(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	adminMux.HandleFunc("/api/admin/service", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			apiAdminAddService(w, r)
		case http.MethodPut:
			apiAdminUpdateService(w, r)
		case http.MethodDelete:
			apiAdminDeleteService(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	adminMux.HandleFunc("/api/admin/service/move", apiAdminMoveService)
	adminMux.HandleFunc("/api/admin/service/reorder", apiAdminReorderServices)

	mux.Handle("/admin", adminAuthMiddleware(adminMux))
	mux.Handle("/api/admin/", adminAuthMiddleware(adminMux))

	srv := httptest.NewServer(mux)

	return srv, func() {
		srv.Close()
		adminAPIKey = oldKey
		configFile = oldConfigFile
	}
}

// authRequest performs an authorized request
func authRequest(method, url string, body any) (*http.Response, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer test-secret-key")
	req.Header.Set("Content-Type", "application/json")

	return http.DefaultClient.Do(req)
}

// ─── Auth tests ───

func TestAdminAuth_NoKey(t *testing.T) {
	oldKey := adminAPIKey
	adminAPIKey = ""
	defer func() { adminAPIKey = oldKey }()

	req := httptest.NewRequest(http.MethodGet, "/api/admin/groups", nil)
	rec := httptest.NewRecorder()

	mux := http.NewServeMux()
	adminMux := http.NewServeMux()
	adminMux.HandleFunc("/api/admin/groups", apiAdminGroups)
	mux.Handle("/api/admin/", adminAuthMiddleware(adminMux))

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
}

func TestAdminAuth_InvalidKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/admin/groups", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	rec := httptest.NewRecorder()

	mux := http.NewServeMux()
	adminMux := http.NewServeMux()
	adminMux.HandleFunc("/api/admin/groups", apiAdminGroups)
	mux.Handle("/api/admin/", adminAuthMiddleware(adminMux))

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
}

func TestAdminAuth_NoAuthHeader(t *testing.T) {
	oldKey := adminAPIKey
	adminAPIKey = "test-key"
	defer func() { adminAPIKey = oldKey }()

	req := httptest.NewRequest(http.MethodGet, "/api/admin/groups", nil)
	rec := httptest.NewRecorder()

	mux := http.NewServeMux()
	adminMux := http.NewServeMux()
	adminMux.HandleFunc("/api/admin/groups", apiAdminGroups)
	mux.Handle("/api/admin/", adminAuthMiddleware(adminMux))

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
	wwwAuth := rec.Header().Get("WWW-Authenticate")
	if wwwAuth == "" {
		t.Error("expected WWW-Authenticate header")
	}
}

// ─── CRUD Group tests ───

func TestAdminAddGroup(t *testing.T) {
	srv, cleanup := setupAdminTest(t)
	defer cleanup()

	resp, err := authRequest(http.MethodPost, srv.URL+"/api/admin/group", map[string]string{
		"name": "NewGroup",
	})
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if result["success"] != true {
		t.Errorf("expected success=true, got %v", result)
	}
}

func TestAdminAddGroup_EmptyName(t *testing.T) {
	srv, cleanup := setupAdminTest(t)
	defer cleanup()

	resp, err := authRequest(http.MethodPost, srv.URL+"/api/admin/group", map[string]string{
		"name": "",
	})
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestAdminAddGroup_Duplicate(t *testing.T) {
	srv, cleanup := setupAdminTest(t)
	defer cleanup()

	// Add first time
	resp1, _ := authRequest(http.MethodPost, srv.URL+"/api/admin/group", map[string]string{
		"name": "DupGroup",
	})
	resp1.Body.Close()

	// Second time — should conflict
	resp2, _ := authRequest(http.MethodPost, srv.URL+"/api/admin/group", map[string]string{
		"name": "DupGroup",
	})
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for duplicate, got %d", resp2.StatusCode)
	}
}

func TestAdminDeleteGroup(t *testing.T) {
	srv, cleanup := setupAdminTest(t)
	defer cleanup()

	// First add
	resp1, _ := authRequest(http.MethodPost, srv.URL+"/api/admin/group", map[string]string{
		"name": "ToDelete",
	})
	resp1.Body.Close()

	// Now delete
	resp2, err := authRequest(http.MethodDelete, srv.URL+"/api/admin/group", map[string]string{
		"name": "ToDelete",
	})
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("expected 200, got %d: %s", resp2.StatusCode, string(body))
	}

	// Verify the group was deleted
	resp3, _ := authRequest(http.MethodGet, srv.URL+"/api/admin/groups", nil)
	defer resp3.Body.Close()

	var result map[string]any
	json.NewDecoder(resp3.Body).Decode(&result)
	groups := result["groups"].([]any)
	for _, g := range groups {
		gm := g.(map[string]any)
		if gm["name"] == "ToDelete" {
			t.Error("group should have been deleted")
		}
	}
}

func TestAdminRenameGroup(t *testing.T) {
	srv, cleanup := setupAdminTest(t)
	defer cleanup()

	// Add group
	resp1, _ := authRequest(http.MethodPost, srv.URL+"/api/admin/group", map[string]string{
		"name": "OldName",
	})
	resp1.Body.Close()

	// Rename
	resp2, err := authRequest(http.MethodPut, srv.URL+"/api/admin/group", map[string]string{
		"old_name": "OldName",
		"new_name": "NewName",
	})
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("expected 200, got %d: %s", resp2.StatusCode, string(body))
	}

	// Verify
	resp3, _ := authRequest(http.MethodGet, srv.URL+"/api/admin/groups", nil)
	defer resp3.Body.Close()

	var result map[string]any
	json.NewDecoder(resp3.Body).Decode(&result)
	groups := result["groups"].([]any)
	found := false
	for _, g := range groups {
		gm := g.(map[string]any)
		if gm["name"] == "NewName" {
			found = true
		}
	}
	if !found {
		t.Error("group was not renamed")
	}
}

// ─── CRUD Service tests ───

func TestAdminAddService(t *testing.T) {
	srv, cleanup := setupAdminTest(t)
	defer cleanup()

	resp, err := authRequest(http.MethodPost, srv.URL+"/api/admin/service", map[string]any{
		"group_name": "TestGroup",
		"service": map[string]any{
			"name": "TestService",
			"url":  "http://localhost:8080",
		},
	})
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}
}

func TestAdminAddService_MissingGroup(t *testing.T) {
	srv, cleanup := setupAdminTest(t)
	defer cleanup()

	resp, _ := authRequest(http.MethodPost, srv.URL+"/api/admin/service", map[string]any{
		"group_name": "NonExistent",
		"service": map[string]any{
			"name": "Svc",
			"url":  "http://localhost",
		},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestAdminDeleteService(t *testing.T) {
	srv, cleanup := setupAdminTest(t)
	defer cleanup()

	// Add service
	resp1, _ := authRequest(http.MethodPost, srv.URL+"/api/admin/service", map[string]any{
		"group_name": "TestGroup",
		"service": map[string]any{
			"name": "ToDelete",
			"url":  "http://localhost:9090",
		},
	})
	resp1.Body.Close()

	// Delete
	resp2, err := authRequest(http.MethodDelete, srv.URL+"/api/admin/service", map[string]string{
		"group_name":   "TestGroup",
		"service_name": "ToDelete",
	})
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("expected 200, got %d: %s", resp2.StatusCode, string(body))
	}
}

func TestAdminUpdateService(t *testing.T) {
	srv, cleanup := setupAdminTest(t)
	defer cleanup()

	// Add service
	resp1, _ := authRequest(http.MethodPost, srv.URL+"/api/admin/service", map[string]any{
		"group_name": "TestGroup",
		"service": map[string]any{
			"name": "OldService",
			"url":  "http://localhost:7070",
		},
	})
	resp1.Body.Close()

	// Update
	resp2, err := authRequest(http.MethodPut, srv.URL+"/api/admin/service", map[string]any{
		"group_name": "TestGroup",
		"old_name":   "OldService",
		"new_service": map[string]any{
			"name": "UpdatedService",
			"url":  "http://localhost:7071",
			"ip":   "127.0.0.1",
		},
	})
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("expected 200, got %d: %s", resp2.StatusCode, string(body))
	}
}

// ─── Move and Reorder tests ───

func TestAdminMoveService(t *testing.T) {
	srv, cleanup := setupAdminTest(t)
	defer cleanup()

	// Add second group
	resp1, _ := authRequest(http.MethodPost, srv.URL+"/api/admin/group", map[string]string{
		"name": "TargetGroup",
	})
	resp1.Body.Close()

	// Add service to first group
	resp2, _ := authRequest(http.MethodPost, srv.URL+"/api/admin/service", map[string]any{
		"group_name": "TestGroup",
		"service": map[string]any{
			"name": "MoveMe",
			"url":  "http://localhost:6060",
		},
	})
	resp2.Body.Close()

	// Move
	resp3, err := authRequest(http.MethodPost, srv.URL+"/api/admin/service/move", map[string]string{
		"from_group": "TestGroup",
		"to_group":   "TargetGroup",
		"service":    "MoveMe",
	})
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp3.Body.Close()

	if resp3.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp3.Body)
		t.Fatalf("expected 200, got %d: %s", resp3.StatusCode, string(body))
	}
}

func TestAdminReorderServices(t *testing.T) {
	srv, cleanup := setupAdminTest(t)
	defer cleanup()

	// Add two services
	resp1, _ := authRequest(http.MethodPost, srv.URL+"/api/admin/service", map[string]any{
		"group_name": "TestGroup",
		"service":    map[string]string{"name": "SvcA", "url": "http://a"},
	})
	resp1.Body.Close()

	resp2, _ := authRequest(http.MethodPost, srv.URL+"/api/admin/service", map[string]any{
		"group_name": "TestGroup",
		"service":    map[string]string{"name": "SvcB", "url": "http://b"},
	})
	resp2.Body.Close()

	// Reorder
	resp3, err := authRequest(http.MethodPost, srv.URL+"/api/admin/service/reorder", map[string]any{
		"group_name": "TestGroup",
		"services":   []string{"SvcB", "SvcA"},
	})
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp3.Body.Close()

	if resp3.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp3.Body)
		t.Fatalf("expected 200, got %d: %s", resp3.StatusCode, string(body))
	}
}

// ─── serveAdmin tests ───

func TestServeAdmin_PathCheck(t *testing.T) {
	oldKey := adminAPIKey
	adminAPIKey = "test-key"
	defer func() { adminAPIKey = oldKey }()

	// Save and restore adminTmpl
	oldAdminTmpl := adminTmpl
	defer func() { adminTmpl = oldAdminTmpl }()

	adminTmpl = nil // will be set below

	// Verify serveAdmin returns 404 for wrong path
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/other", nil)

	// serveAdmin should return 404
	serveAdmin(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}

	adminTmpl = oldAdminTmpl
}

// ─── Middleware tests ───

func TestMaxBytesMiddleware(t *testing.T) {
	handler := maxBytesMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Try to read body — should get size exceeded error here
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			return
		}
		_ = data
		w.WriteHeader(http.StatusOK)
	}))

	// Large body (2MB > 1MB limit)
	largeBody := make([]byte, 2<<20)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(largeBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d", rec.Code)
	}
}

func TestContentTypeMiddleware_Valid(t *testing.T) {
	called := false
	handler := contentTypeMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if !called {
		t.Error("handler was not called")
	}
}

func TestContentTypeMiddleware_Invalid(t *testing.T) {
	handler := contentTypeMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("expected 415, got %d", rec.Code)
	}
}

func TestCORSHeaders(t *testing.T) {
	handler := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("missing CORS Allow-Origin header")
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204 for OPTIONS, got %d", rec.Code)
	}
}
