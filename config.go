package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// configData holds loaded config.
type configData struct {
	groups []Group
}

// loadConfig loads groups from the configuration file.
func loadConfig(configFile string) (*configData, error) {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("чтение %s: %w", configFile, err)
	}

	// Format with groups.
	var root struct {
		Groups []Group `json:"groups"`
	}
	if err := json.Unmarshal(data, &root); err == nil && len(root.Groups) > 0 {
		return &configData{groups: root.Groups}, nil
	}

	// Flat list of services.
	var svcs []Service
	if err := json.Unmarshal(data, &svcs); err == nil && len(svcs) > 0 {
		return &configData{
			groups: []Group{{Name: "Все сервисы", Services: svcs}},
		}, nil
	}

	return nil, fmt.Errorf("неверный формат: ожидается 'groups' или список сервисов")
}

// loadGroups loads groups from the configuration file.
func loadGroups(configFile string) ([]Group, error) {
	cfg, err := loadConfig(configFile)
	if err != nil {
		return nil, err
	}
	return cfg.groups, nil
}

// validateGroups validates the group structure. Service names must be unique
// across the whole configuration because their names are used as status keys.
func validateGroups(groups []Group) error {
	if len(groups) == 0 {
		return fmt.Errorf("список групп пуст")
	}

	groupNames := make(map[string]bool, len(groups))
	serviceNames := make(map[string]string) // lowercase name -> group name

	for i, g := range groups {
		if strings.TrimSpace(g.Name) == "" {
			return fmt.Errorf("группа #%d: пустое имя", i)
		}
		groupKey := strings.ToLower(strings.TrimSpace(g.Name))
		if groupNames[groupKey] {
			return fmt.Errorf("дубликат имени группы: %q", g.Name)
		}
		groupNames[groupKey] = true

		for j, s := range g.Services {
			if strings.TrimSpace(s.Name) == "" {
				return fmt.Errorf("группа %q, сервис #%d: пустое имя", g.Name, j)
			}
			if strings.TrimSpace(s.URL) == "" && strings.TrimSpace(s.IP) == "" {
				return fmt.Errorf("группа %q, сервис %q: не указан URL или IP", g.Name, s.Name)
			}
			if err := validateURL(s.URL, fmt.Sprintf("группа %q, сервис %q: URL", g.Name, s.Name)); err != nil {
				return err
			}
			if err := validateIP(s.IP, fmt.Sprintf("группа %q, сервис %q: IP", g.Name, s.Name)); err != nil {
				return err
			}

			key := strings.ToLower(strings.TrimSpace(s.Name))
			if prevGroup, ok := serviceNames[key]; ok {
				return fmt.Errorf("сервис %q уже существует в группе %q (имена сервисов должны быть уникальны)", s.Name, prevGroup)
			}
			serviceNames[key] = g.Name
		}
	}
	return nil
}

// cloneGroups returns a deep copy of groups.
func cloneGroups(groups []Group) []Group {
	result := make([]Group, len(groups))
	for i, g := range groups {
		result[i] = Group{
			Name:     g.Name,
			Services: make([]Service, len(g.Services)),
		}
		copy(result[i].Services, g.Services)
	}
	return result
}

// saveGroupsToFile saves groups to config.json with an atomic write.
func saveGroupsToFile(configFile string, groups []Group) error {
	return saveConfigToFile(configFile, groups)
}

// saveConfigToFile saves groups with an atomic write, preserving the existing
// file mode and syncing data before the rename.
func saveConfigToFile(configFile string, groups []Group) error {
	data := struct {
		Groups []Group `json:"groups"`
	}{
		Groups: groups,
	}

	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}
	jsonData = append(jsonData, '\n')

	mode := os.FileMode(0o644)
	if info, err := os.Stat(configFile); err == nil {
		mode = info.Mode().Perm()
	}

	dir := filepath.Dir(configFile)
	tmpFile, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmpFile.Name()

	cleanup := func() {
		tmpFile.Close()
		os.Remove(tmpName)
	}

	if err := tmpFile.Chmod(mode); err != nil {
		cleanup()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmpFile.Write(jsonData); err != nil {
		cleanup()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync temp file: %w", err)
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
