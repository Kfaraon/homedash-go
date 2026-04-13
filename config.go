package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// loadGroups loads groups from the configuration file
func loadGroups(configFile string) ([]Group, error) {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("чтение %s: %w", configFile, err)
	}

	// Format with groups
	var root struct{ Groups []Group }
	if err := json.Unmarshal(data, &root); err == nil && len(root.Groups) > 0 {
		return root.Groups, nil
	}

	// Flat list of services
	var svcs []Service
	if err := json.Unmarshal(data, &svcs); err == nil && len(svcs) > 0 {
		return []Group{{Name: "Все сервисы", Services: svcs}}, nil
	}

	return nil, fmt.Errorf("неверный формат: ожидается 'groups' или список сервисов")
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
		// Duplicate map — separate for each group
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
				slog.Warn("Duplicate service name detected", "group", g.Name, "name", s.Name)
			}
			seen[key] = true
		}
	}
	return nil
}

// saveGroupsToFile saves groups to config.json with atomic write
func saveGroupsToFile(configFile string, groups []Group) error {
	data := struct {
		Groups []Group `json:"groups"`
	}{
		Groups: groups,
	}

	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}

	// Add newline at end
	jsonData = append(jsonData, '\n')

	// Atomic write: write to temp file, then rename
	tmpFile := configFile + ".tmp"
	if err := os.WriteFile(tmpFile, jsonData, 0644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tmpFile, configFile); err != nil {
		// If rename failed, try regular write
		slog.Warn("Rename failed, using fallback write", "error", err)
		if err := os.WriteFile(configFile, jsonData, 0644); err != nil {
			return fmt.Errorf("write file (fallback): %w", err)
		}
	}

	return nil
}
