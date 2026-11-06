package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestValidateGroupsRejectsGlobalDuplicates(t *testing.T) {
	groups := []Group{
		{Name: "A", Services: []Service{{Name: "Grafana", URL: "http://a"}}},
		{Name: "B", Services: []Service{{Name: "grafana", URL: "http://b"}}},
	}
	if err := validateGroups(groups); err == nil {
		t.Fatal("expected error for duplicate service name across groups")
	}
}

func TestValidateGroupsRejectsDuplicateGroupNames(t *testing.T) {
	groups := []Group{
		{Name: "A", Services: []Service{{Name: "S1", URL: "http://a"}}},
		{Name: "a", Services: []Service{{Name: "S2", URL: "http://b"}}},
	}
	if err := validateGroups(groups); err == nil {
		t.Fatal("expected error for duplicate group names")
	}
}

func TestValidateGroupsRequiresHost(t *testing.T) {
	groups := []Group{
		{Name: "A", Services: []Service{{Name: "S", URL: "http://"}}},
	}
	if err := validateGroups(groups); err == nil {
		t.Fatal("expected error for URL without host")
	}
}

func TestValidateGroupsAcceptsValidConfig(t *testing.T) {
	if err := validateGroups(defaultGroups()); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}
}

func TestLoadConfigFlatList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`[{"name":"S","url":"http://x"}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.groups) != 1 || cfg.groups[0].Name != "Все сервисы" {
		t.Fatalf("unexpected groups: %+v", cfg.groups)
	}
}

func TestSaveConfigPreservesMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeConfig(t, path, defaultGroups())
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}

	if err := saveGroupsToFile(path, defaultGroups()); err != nil {
		t.Fatalf("saveGroupsToFile: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("mode = %o, want 640", got)
	}
}

func TestNewAppRejectsInvalidEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeConfig(t, path, defaultGroups())
	t.Setenv("CONFIG_FILE", path)
	t.Setenv("ICONS_CONFIG_PATH", "data/icons.json")
	t.Setenv("LAZY_CHECK_INTERVAL", "0s")

	if _, err := NewApp(); err == nil {
		t.Fatal("expected error for zero LAZY_CHECK_INTERVAL")
	}
}

func TestNewAppRejectsInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeConfig(t, path, []Group{{Name: "A", Services: []Service{{Name: "S"}}}})
	t.Setenv("CONFIG_FILE", path)
	t.Setenv("ICONS_CONFIG_PATH", "data/icons.json")

	if _, err := NewApp(); err == nil {
		t.Fatal("expected error for service without URL/IP")
	}
}

func TestConfigWatcherReloadsNestedConfig(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "data")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(sub, "config.json")
	writeConfig(t, cfg, defaultGroups())

	t.Setenv("CONFIG_FILE", cfg)
	t.Setenv("ICONS_CONFIG_PATH", "data/icons.json")
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

	app.StartConfigWatcher()

	updated := append(defaultGroups(), Group{Name: "Added", Services: []Service{{Name: "Second", URL: "http://127.0.0.1:2"}}})
	writeConfig(t, cfg, updated)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if app.GetGroupsCount() == 2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("config reload did not happen, groups=%d", app.GetGroupsCount())
}
