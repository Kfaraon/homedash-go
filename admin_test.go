package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func readGroups(t *testing.T, path string) []Group {
	t.Helper()
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	return cfg.groups
}

func TestAdminNoAuth(t *testing.T) {
	_, h := newTestRouter(t, defaultGroups())
	rr := doJSON(t, h, http.MethodGet, "/api/admin/groups", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin API without auth: got %d, want 200", rr.Code)
	}
}

func TestAddAndUpdateService(t *testing.T) {
	app, h := newTestRouter(t, defaultGroups())

	rr := doJSON(t, h, http.MethodPost, "/api/admin/service",
		`{"group_name":"Test","service":{"name":"New","url":"http://127.0.0.1:9","icon":"simple-icons:test"}}`, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("add service: got %d (%s)", rr.Code, rr.Body.String())
	}

	// Update without icon must preserve it.
	rr = doJSON(t, h, http.MethodPut, "/api/admin/service",
		`{"group_name":"Test","old_name":"New","new_service":{"name":"New","url":"http://127.0.0.1:10"}}`, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("update service: got %d (%s)", rr.Code, rr.Body.String())
	}

	groups := readGroups(t, app.ConfigFile)
	var found *Service
	for i := range groups {
		for j := range groups[i].Services {
			if groups[i].Services[j].Name == "New" {
				found = &groups[i].Services[j]
			}
		}
	}
	if found == nil {
		t.Fatal("updated service not found")
	}
	if found.Icon != "simple-icons:test" {
		t.Fatalf("icon not preserved: %q", found.Icon)
	}
	if found.URL != "http://127.0.0.1:10" {
		t.Fatalf("url = %q", found.URL)
	}
}

func TestUpdateServiceRejectsEmptyTarget(t *testing.T) {
	_, h := newTestRouter(t, defaultGroups())
	rr := doJSON(t, h, http.MethodPut, "/api/admin/service",
		`{"group_name":"Test","old_name":"Local","new_service":{"name":"Local","url":"","ip":""}}`, nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", rr.Code, rr.Body.String())
	}
}

func TestDuplicateServiceNameAcrossGroupsRejected(t *testing.T) {
	groups := []Group{
		{Name: "A", Services: []Service{{Name: "One", URL: "http://a"}}},
		{Name: "B", Services: []Service{{Name: "Two", URL: "http://b"}}},
	}
	_, h := newTestRouter(t, groups)

	rr := doJSON(t, h, http.MethodPost, "/api/admin/service",
		`{"group_name":"B","service":{"name":"One","url":"http://c"}}`, nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for global duplicate, got %d (%s)", rr.Code, rr.Body.String())
	}
}

func TestReorderRejectsDuplicates(t *testing.T) {
	groups := []Group{{Name: "A", Services: []Service{
		{Name: "One", URL: "http://a"},
		{Name: "Two", URL: "http://b"},
	}}}
	_, h := newTestRouter(t, groups)

	rr := doJSON(t, h, http.MethodPost, "/api/admin/service/reorder",
		`{"group_name":"A","services":["One","One"]}`, nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for duplicate reorder, got %d (%s)", rr.Code, rr.Body.String())
	}
}

func TestMoveServiceRejectsSameGroupAndDuplicate(t *testing.T) {
	groups := []Group{
		{Name: "A", Services: []Service{{Name: "One", URL: "http://a"}}},
		{Name: "B", Services: []Service{{Name: "Two", URL: "http://b"}}},
	}
	_, h := newTestRouter(t, groups)

	rr := doJSON(t, h, http.MethodPost, "/api/admin/service/move",
		`{"from_group":"A","to_group":"A","service":"One"}`, nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("same group move: expected 400, got %d", rr.Code)
	}

	// Add a duplicate name in B by hand-editing is impossible via API, so test
	// the helper directly.
	if !serviceNameTaken(groups, "Two", 0, 0) {
		t.Fatal("expected serviceNameTaken to detect duplicate in target group")
	}
}

func TestSaveFailureDoesNotChangeState(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to read-only directories")
	}

	app, cfg := newTestApp(t, defaultGroups())
	dir := filepath.Dir(cfg)

	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)

	_, h := app, app.buildRouter()
	rr := doJSON(t, h, http.MethodPost, "/api/admin/group", `{"name":"Ghost"}`, nil)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on save failure, got %d (%s)", rr.Code, rr.Body.String())
	}

	groups := readGroups(t, cfg)
	for _, g := range groups {
		if g.Name == "Ghost" {
			t.Fatal("failed save leaked into memory/disk")
		}
	}
	if app.GetGroupsCount() != len(groups) {
		t.Fatalf("in-memory groups changed after failed save: %d vs %d", app.GetGroupsCount(), len(groups))
	}
}

func TestReorderWorks(t *testing.T) {
	groups := []Group{{Name: "A", Services: []Service{
		{Name: "One", URL: "http://a"},
		{Name: "Two", URL: "http://b"},
	}}}
	app, h := newTestRouter(t, groups)

	rr := doJSON(t, h, http.MethodPost, "/api/admin/service/reorder",
		`{"group_name":"A","services":["Two","One"]}`, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("reorder: got %d (%s)", rr.Code, rr.Body.String())
	}

	got := readGroups(t, app.ConfigFile)[0].Services
	if got[0].Name != "Two" || got[1].Name != "One" {
		t.Fatalf("unexpected order: %+v", got)
	}

	var raw map[string]any
	data, _ := os.ReadFile(app.ConfigFile)
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
}
