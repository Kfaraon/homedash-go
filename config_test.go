package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── loadGroups tests ───

func TestLoadGroups_GroupedFormat(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "config.json")

	data := map[string]any{
		"groups": []map[string]any{
			{
				"name":     "Infrastructure",
				"services": []map[string]any{{"name": "Server1", "url": "http://localhost"}},
			},
		},
	}
	writeJSON(t, cfg, data)

	groups, err := loadGroups(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].Name != "Infrastructure" {
		t.Errorf("expected group name 'Infrastructure', got %q", groups[0].Name)
	}
	if len(groups[0].Services) != 1 {
		t.Errorf("expected 1 service, got %d", len(groups[0].Services))
	}
}

func TestLoadGroups_FlatList(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "config.json")

	services := []map[string]any{
		{"name": "Web", "url": "http://example.com"},
		{"name": "DB", "ip": "127.0.0.1"},
	}
	writeJSON(t, cfg, services)

	groups, err := loadGroups(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].Name != "Все сервисы" {
		t.Errorf("expected default group name, got %q", groups[0].Name)
	}
	if len(groups[0].Services) != 2 {
		t.Errorf("expected 2 services, got %d", len(groups[0].Services))
	}
}

func TestLoadGroups_InvalidFormat(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "config.json")
	writeJSON(t, cfg, map[string]string{"foo": "bar"})

	_, err := loadGroups(cfg)
	if err == nil {
		t.Fatal("expected error for invalid format")
	}
}

func TestLoadGroups_FileNotFound(t *testing.T) {
	_, err := loadGroups("/nonexistent/config.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// ─── validateGroups tests ───

func TestValidateGroups_EmptyList(t *testing.T) {
	err := validateGroups([]Group{})
	if err == nil {
		t.Fatal("expected error for empty groups")
	}
}

func TestValidateGroups_EmptyGroupName(t *testing.T) {
	err := validateGroups([]Group{{Name: "", Services: []Service{}}})
	if err == nil {
		t.Fatal("expected error for empty group name")
	}
}

func TestValidateGroups_EmptyServiceName(t *testing.T) {
	err := validateGroups([]Group{
		{Name: "Test", Services: []Service{{Name: "", URL: "http://x"}}},
	})
	if err == nil {
		t.Fatal("expected error for empty service name")
	}
}

func TestValidateGroups_MissingURLAndIP(t *testing.T) {
	err := validateGroups([]Group{
		{Name: "Test", Services: []Service{{Name: "Svc", URL: "", IP: ""}}},
	})
	if err == nil {
		t.Fatal("expected error when both URL and IP are missing")
	}
}

func TestValidateGroups_DuplicateNames(t *testing.T) {
	// validateGroups only warns (logs), doesn't return error
	groups := []Group{
		{
			Name: "Test",
			Services: []Service{
				{Name: "Svc", URL: "http://a"},
				{Name: "svc", URL: "http://b"}, // same name, different case
			},
		},
	}
	err := validateGroups(groups)
	if err != nil {
		t.Errorf("expected no error (only warning), got: %v", err)
	}
}

func TestValidateGroups_ValidGroups(t *testing.T) {
	groups := []Group{
		{
			Name: "Infra",
			Services: []Service{
				{Name: "Web", URL: "http://localhost"},
				{Name: "DB", IP: "127.0.0.1"},
			},
		},
		{
			Name:     "Empty",
			Services: []Service{},
		},
	}
	err := validateGroups(groups)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

// ─── saveGroupsToFile tests ───

func TestSaveGroupsToFile_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "config.json")

	original := []Group{
		{
			Name: "Test",
			Services: []Service{
				{Name: "Web", URL: "http://localhost", VerifySSL: true},
			},
		},
	}

	if err := saveGroupsToFile(cfg, original); err != nil {
		t.Fatalf("save error: %v", err)
	}

	loaded, err := loadGroups(cfg)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}

	if len(loaded) != 1 || loaded[0].Name != "Test" {
		t.Errorf("round-trip mismatch: %+v", loaded)
	}
	if len(loaded[0].Services) != 1 || loaded[0].Services[0].Name != "Web" {
		t.Errorf("service round-trip mismatch: %+v", loaded)
	}
}

func TestSaveGroupsToFile_JSONFormat(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "config.json")

	groups := []Group{{Name: "A", Services: []Service{}}}
	if err := saveGroupsToFile(cfg, groups); err != nil {
		t.Fatalf("save error: %v", err)
	}

	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}

	var root struct{ Groups []Group }
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(root.Groups) != 1 {
		t.Errorf("expected groups wrapper, got: %s", string(data))
	}
}

// ─── helpers ───

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	// Ensure no trailing newline issues
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write error: %v", err)
	}
}

func init() {
	// Ensure strings package is referenced
	_ = strings.TrimSpace
}
