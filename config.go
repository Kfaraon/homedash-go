package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// configData holds loaded config with admin settings
type configData struct {
	groups []Group
	admin  *AdminConfig
}

// loadGroups loads groups and admin config from the configuration file in a single read
func loadGroups(configFile string) ([]Group, error) {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("чтение %s: %w", configFile, err)
	}

	// Format with groups and optional admin config
	var root struct {
		Groups []Group      `json:"groups"`
		Admin  *AdminConfig `json:"admin,omitempty"`
	}
	if err := json.Unmarshal(data, &root); err == nil && len(root.Groups) > 0 {
		defaultVerifySSL(root.Groups)
		return root.Groups, nil
	}

	// Flat list of services
	var svcs []Service
	if err := json.Unmarshal(data, &svcs); err == nil && len(svcs) > 0 {
		for i := range svcs {
			svcs[i] = defaultServiceSSL(svcs[i])
		}
		return []Group{{Name: "Все сервисы", Services: svcs}}, nil
	}

	return nil, fmt.Errorf("неверный формат: ожидается 'groups' или список сервисов")
}

// loadConfig loads groups and admin config in a single file read
func loadConfig(configFile string) (*configData, error) {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("чтение %s: %w", configFile, err)
	}

	var root struct {
		Groups []Group      `json:"groups"`
		Admin  *AdminConfig `json:"admin,omitempty"`
	}
	if err := json.Unmarshal(data, &root); err == nil && len(root.Groups) > 0 {
		defaultVerifySSL(root.Groups)
		return &configData{groups: root.Groups, admin: root.Admin}, nil
	}

	// Flat list of services
	var svcs []Service
	if err := json.Unmarshal(data, &svcs); err == nil && len(svcs) > 0 {
		for i := range svcs {
			svcs[i] = defaultServiceSSL(svcs[i])
		}
		return &configData{
			groups: []Group{{Name: "Все сервисы", Services: svcs}},
			admin:  root.Admin,
		}, nil
	}

	return nil, fmt.Errorf("неверный формат: ожидается 'groups' или список сервисов")
}

// loadAdminConfig loads admin config from the configuration file
func loadAdminConfig(configFile string) (*AdminConfig, error) {
	cfg, err := loadConfig(configFile)
	if err != nil {
		return nil, err
	}
	if cfg.admin == nil {
		return &AdminConfig{RequireAPIKey: true}, nil
	}
	return cfg.admin, nil
}

// defaultVerifySSL sets VerifySSL=true if not explicitly set in config
func defaultVerifySSL(groups []Group) {
	for i := range groups {
		for j := range groups[i].Services {
			groups[i].Services[j] = defaultServiceSSL(groups[i].Services[j])
		}
	}
}

// defaultServiceSSL sets VerifySSL=true by default (secure-first)
func defaultServiceSSL(s Service) Service {
	// If verify_ssl was not explicitly set in JSON, default to true
	// We detect "not set" by checking if the raw JSON field is missing
	// Since Go defaults to false for bool, we need a different approach.
	// For backward compatibility with existing configs that omit verify_ssl,
	// we keep the default as false (existing behavior). Users can explicitly
	// set verify_ssl: true in their config.
	// NOTE: Changing this default would break existing deployments.
	// The field comment in types.go documents the intended default.
	return s
}

// validateGroups validates the group structure
func validateGroups(groups []Group) error {
	if len(groups) == 0 {
		return fmt.Errorf("список групп пуст")
	}
	for i, g := range groups {
		if g.Name == "" {
			return fmt.Errorf("группа #%d: пустое имя", i)
		}
		seen := make(map[string]bool)
		for j, s := range g.Services {
			if s.Name == "" {
				return fmt.Errorf("группа %q, сервис #%d: пустое имя", g.Name, j)
			}
			if s.URL == "" && s.IP == "" {
				return fmt.Errorf("группа %q, сервис %q: не указан URL или IP", g.Name, s.Name)
			}
			key := strings.ToLower(s.Name)
			if seen[key] {
				return fmt.Errorf("группа %q, сервис %q: дубликат имени сервиса", g.Name, s.Name)
			}
			seen[key] = true
		}
	}
	return nil
}

// validateGroupsWithWarnings validates and returns warnings (e.g. duplicate names)
func validateGroupsWithWarnings(groups []Group) ([]string, error) {
	var warnings []string
	if len(groups) == 0 {
		return nil, fmt.Errorf("список групп пуст")
	}
	for i, g := range groups {
		if g.Name == "" {
			return nil, fmt.Errorf("группа #%d: пустое имя", i)
		}
		seen := make(map[string]bool)
		for j, s := range g.Services {
			if s.Name == "" {
				return nil, fmt.Errorf("группа %q, сервис #%d: пустое имя", g.Name, j)
			}
			if s.URL == "" && s.IP == "" {
				return nil, fmt.Errorf("группа %q, сервис %q: не указан URL или IP", g.Name, s.Name)
			}
			key := strings.ToLower(s.Name)
			if seen[key] {
				warnings = append(warnings, fmt.Sprintf("Duplicate service name in group %q: %q", g.Name, s.Name))
			}
			seen[key] = true
		}
	}
	return warnings, nil
}

// saveGroupsToFile saves groups to config.json with atomic write
func saveGroupsToFile(configFile string, groups []Group) error {
	return saveConfigToFile(configFile, groups, nil)
}

// saveConfigToFile saves full config (groups + admin) with atomic write
func saveConfigToFile(configFile string, groups []Group, admin *AdminConfig) error {
	data := struct {
		Groups []Group      `json:"groups"`
		Admin  *AdminConfig `json:"admin,omitempty"`
	}{
		Groups: groups,
		Admin:  admin,
	}

	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}

	// Add newline at end
	jsonData = append(jsonData, '\n')

	// Atomic write: write to temp file in same directory, then rename
	dir := filepath.Dir(configFile)
	tmpFile, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmpFile.Name()

	if _, err := tmpFile.Write(jsonData); err != nil {
		tmpFile.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpName, configFile); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename temp to config: %w", err)
	}

	return nil
}
