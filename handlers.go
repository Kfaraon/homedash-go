package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// nameValidation — допустимые символы в именах (буквы, цифры, пробелы, дефисы, подчёркивания, точки)
var nameRegex = regexp.MustCompile(`^[a-zA-Zа-яА-ЯёЁ0-9 _\-.]+$`)

const maxNameLength = 100

// validateName checks name for safety and length
func validateName(name, label string) error {
	if len(strings.TrimSpace(name)) == 0 {
		return fmt.Errorf("%s is required", label)
	}
	if len(name) > maxNameLength {
		return fmt.Errorf("%s must be at most %d characters", label, maxNameLength)
	}
	if !nameRegex.MatchString(name) {
		return fmt.Errorf("%s contains invalid characters (only letters, digits, spaces, hyphens, underscores, dots allowed)", label)
	}
	return nil
}

// validateURL checks URL correctness
func validateURL(u, label string) error {
	if u == "" {
		return nil // URL is optional
	}
	if _, err := url.ParseRequestURI(u); err != nil {
		return fmt.Errorf("%s is not a valid URL: %w", label, err)
	}
	return nil
}

// serveHome — handler for the main page
func serveHome(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		state.groupsMu.RLock()
		g := state.groups
		state.groupsMu.RUnlock()

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.ExecuteTemplate(w, "home.html", map[string]any{"groups": g}); err != nil {
			slog.Error("Template rendering error", "groups_count", len(g), "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// serveStatus — handler for status API
func serveStatus(w http.ResponseWriter, r *http.Request) {
	state.groupsMu.RLock()
	g := state.groups
	state.groupsMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(getCachedStatuses(g))
}

// serveHealth — handler for health check
func serveHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// Check config file
	configOK := true
	if _, err := loadGroups(configFile); err != nil {
		slog.Warn("Health check: config load failed", "error", err)
		configOK = false
	}

	status := "ok"
	if !configOK {
		status = "degraded"
	}

	json.NewEncoder(w).Encode(map[string]any{
		"status":        status,
		"config_ok":     configOK,
		"cache_entries": len(state.cache),
		"groups_count":  len(state.groups),
		"timestamp":     time.Now().Format(time.RFC3339),
	})
}

// serveMetrics — handler for metrics (JSON format for frontend)
func serveMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics.GetSnapshot())
}

// servePrometheusMetrics — handler for Prometheus metrics (text format)
func servePrometheusMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Write([]byte(metrics.GetPrometheusMetrics()))
}

// corsMiddleware — middleware for CORS
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		if allowedOrigins == "" {
			// Allow all by default (backward compatibility)
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else if origin != "" {
			// Check specific origin
			for _, ao := range strings.Split(allowedOrigins, ",") {
				ao = strings.TrimSpace(ao)
				if origin == ao {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Vary", "Origin")
					break
				}
			}
		}

		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// adminAuthMiddleware — API key check for admin endpoints
func adminAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if adminAPIKey == "" {
			http.Error(w, "Admin panel is disabled. Set ADMIN_API_KEY environment variable to enable.", http.StatusForbidden)
			return
		}

		auth := r.Header.Get("Authorization")
		if auth == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="admin"`)
			http.Error(w, "Authorization required", http.StatusUnauthorized)
			return
		}

		// Support "Bearer <key>" format
		if strings.HasPrefix(auth, "Bearer ") {
			auth = strings.TrimPrefix(auth, "Bearer ")
		}

		if auth != adminAPIKey {
			http.Error(w, "Invalid API key", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// maxBytesMiddleware — request body size limit (1MB)
func maxBytesMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB
		}
		next.ServeHTTP(w, r)
	})
}

// contentTypeMiddleware — Content-Type check for POST/PUT requests
func contentTypeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			contentType := r.Header.Get("Content-Type")
			if contentType != "" && !strings.Contains(contentType, "application/json") {
				http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ===== Cached Statuses with stale-while-revalidate =====

func getCachedStatuses(groups []Group) map[string]any {
	state.cacheMu.RLock()
	// Check main cache validity
	if time.Since(state.cacheTS) < cacheTTL && state.cache != nil {
		defer state.cacheMu.RUnlock()
		metrics.IncrementCacheHits()
		return statusResp(state.cache)
	}
	state.cacheMu.RUnlock()

	// Stale-while-revalidate: return stale cache while refreshing
	state.cacheMu.RLock()
	hasStale := len(state.staleCache) > 0 && time.Since(state.staleCacheTS) < cacheTTL*5
	if hasStale {
		state.cacheMu.RUnlock()
		metrics.IncrementCacheHits()
		// Background refresh with separate context (not cancelled by request)
		go refreshCache(context.Background(), groups)
		return statusResp(state.staleCache)
	}
	state.cacheMu.RUnlock()

	// Blocking update
	state.cacheMu.Lock()
	defer state.cacheMu.Unlock()

	// Re-check after acquiring lock
	if time.Since(state.cacheTS) < cacheTTL && state.cache != nil {
		metrics.IncrementCacheHits()
		return statusResp(state.cache)
	}

	metrics.IncrementCacheMisses()
	// Use background context with timeout instead of request context,
	// so request cancellation doesn't abort service checks
	checkCtx, cancel := context.WithTimeout(context.Background(), time.Duration(len(groups)*2)*time.Second)
	defer cancel()
	sm := checkServicesInParallel(checkCtx, groups, metrics)

	state.cache = sm
	state.cacheTS = time.Now()

	// Save stale copy
	state.staleCache = make(map[string]Status, len(sm))
	for k, v := range sm {
		state.staleCache[k] = v
	}
	state.staleCacheTS = time.Now()

	return statusResp(sm)
}

// refreshCache background cache refresh
func refreshCache(_ context.Context, groups []Group) {
	// Background context with timeout
	checkCtx, cancel := context.WithTimeout(context.Background(), time.Duration(len(groups)*2)*time.Second)
	defer cancel()

	sm := checkServicesInParallel(checkCtx, groups, metrics)

	state.cacheMu.Lock()
	state.cache = sm
	state.cacheTS = time.Now()
	state.staleCache = make(map[string]Status, len(sm))
	for k, v := range sm {
		state.staleCache[k] = v
	}
	state.staleCacheTS = time.Now()
	state.cacheMu.Unlock()
}

func statusResp(services map[string]Status) map[string]any {
	return map[string]any{
		"services":  services,
		"timestamp": time.Now().Format(time.RFC3339),
	}
}

// ===== Admin handlers =====

// serveAdmin — displays admin page
func serveAdmin(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/admin" {
		http.NotFound(w, r)
		return
	}

	state.groupsMu.RLock()
	groups := make([]Group, len(state.groups))
	copy(groups, state.groups)
	state.groupsMu.RUnlock()

	groupsJSON, err := json.Marshal(groups)
	if err != nil {
		slog.Error("Error marshaling admin groups", "error", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := adminTmpl.ExecuteTemplate(w, "admin.html", AdminData{
		Groups:     groups,
		GroupsJSON: template.JS(groupsJSON),
	}); err != nil {
		slog.Error("Admin template rendering error", "groups_count", len(groups), "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// apiAdminGroups — GET /api/admin/groups
func apiAdminGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	state.groupsMu.RLock()
	groups := make([]Group, len(state.groups))
	copy(groups, state.groups)
	state.groupsMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"groups": groups,
	})
}

// apiAdminAddGroup — POST /api/admin/group
func apiAdminAddGroup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		Name string `json:"name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if err := validateName(payload.Name, "Group name"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	state.groupsMu.Lock()
	defer state.groupsMu.Unlock()

	for _, g := range state.groups {
		if strings.EqualFold(g.Name, payload.Name) {
			http.Error(w, "Group with this name already exists", http.StatusBadRequest)
			return
		}
	}

	newGroup := Group{
		Name:     payload.Name,
		Services: []Service{},
	}

	state.groups = append(state.groups, newGroup)

	if err := saveGroupsToFile(configFile, state.groups); err != nil {
		http.Error(w, fmt.Sprintf("Save error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Group added",
	})
}

// apiAdminDeleteGroup — DELETE /api/admin/group
func apiAdminDeleteGroup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		Name string `json:"name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if err := validateName(payload.Name, "Group name"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	state.groupsMu.Lock()
	defer state.groupsMu.Unlock()

	found := false
	newGroups := make([]Group, 0, len(state.groups))
	for _, g := range state.groups {
		if strings.EqualFold(g.Name, payload.Name) {
			found = true
			continue
		}
		newGroups = append(newGroups, g)
	}

	if !found {
		http.Error(w, "Group not found", http.StatusNotFound)
		return
	}

	state.groups = newGroups

	if err := saveGroupsToFile(configFile, state.groups); err != nil {
		http.Error(w, fmt.Sprintf("Save error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Group deleted",
	})
}

// apiAdminRenameGroup — PUT /api/admin/group
func apiAdminRenameGroup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		OldName string `json:"old_name"`
		NewName string `json:"new_name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if err := validateName(payload.OldName, "Old group name"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateName(payload.NewName, "New group name"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	state.groupsMu.Lock()
	defer state.groupsMu.Unlock()

	for _, g := range state.groups {
		if strings.EqualFold(g.Name, payload.NewName) && !strings.EqualFold(g.Name, payload.OldName) {
			http.Error(w, "Group with this name already exists", http.StatusBadRequest)
			return
		}
	}

	found := false
	for i, g := range state.groups {
		if strings.EqualFold(g.Name, payload.OldName) {
			state.groups[i].Name = payload.NewName
			found = true
			break
		}
	}

	if !found {
		http.Error(w, "Group not found", http.StatusNotFound)
		return
	}

	if err := saveGroupsToFile(configFile, state.groups); err != nil {
		http.Error(w, fmt.Sprintf("Save error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Group renamed",
	})
}

// apiAdminAddService — POST /api/admin/service
func apiAdminAddService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		GroupName string  `json:"group_name"`
		Service   Service `json:"service"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if err := validateName(payload.GroupName, "Group name"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := validateName(payload.Service.Name, "Service name"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(payload.Service.URL) == "" && strings.TrimSpace(payload.Service.IP) == "" {
		http.Error(w, "Either URL or IP must be specified", http.StatusBadRequest)
		return
	}

	if err := validateURL(payload.Service.URL, "Service URL"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	state.groupsMu.Lock()
	defer state.groupsMu.Unlock()

	groupIdx := -1
	for i, g := range state.groups {
		if strings.EqualFold(g.Name, payload.GroupName) {
			groupIdx = i
			break
		}
	}

	if groupIdx == -1 {
		http.Error(w, "Group not found", http.StatusNotFound)
		return
	}

	state.groups[groupIdx].Services = append(state.groups[groupIdx].Services, payload.Service)

	if err := saveGroupsToFile(configFile, state.groups); err != nil {
		http.Error(w, fmt.Sprintf("Save error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Service added",
	})
}

// apiAdminUpdateService — PUT /api/admin/service
func apiAdminUpdateService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		GroupName  string  `json:"group_name"`
		OldName    string  `json:"old_name"`
		NewService Service `json:"new_service"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if err := validateName(payload.GroupName, "Group name"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateName(payload.OldName, "Old service name"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateName(payload.NewService.Name, "New service name"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateURL(payload.NewService.URL, "Service URL"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	state.groupsMu.Lock()
	defer state.groupsMu.Unlock()

	groupIdx := -1
	for i, g := range state.groups {
		if strings.EqualFold(g.Name, payload.GroupName) {
			groupIdx = i
			break
		}
	}

	if groupIdx == -1 {
		http.Error(w, "Group not found", http.StatusNotFound)
		return
	}

	serviceIdx := -1
	for i, s := range state.groups[groupIdx].Services {
		if s.Name == payload.OldName {
			serviceIdx = i
			break
		}
	}

	if serviceIdx == -1 {
		http.Error(w, "Service not found", http.StatusNotFound)
		return
	}

	state.groups[groupIdx].Services[serviceIdx] = payload.NewService

	if err := saveGroupsToFile(configFile, state.groups); err != nil {
		http.Error(w, fmt.Sprintf("Save error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Service updated",
	})
}

// apiAdminDeleteService — DELETE /api/admin/service
func apiAdminDeleteService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		GroupName   string `json:"group_name"`
		ServiceName string `json:"service_name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if err := validateName(payload.GroupName, "Group name"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateName(payload.ServiceName, "Service name"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	state.groupsMu.Lock()
	defer state.groupsMu.Unlock()

	groupIdx := -1
	for i, g := range state.groups {
		if strings.EqualFold(g.Name, payload.GroupName) {
			groupIdx = i
			break
		}
	}

	if groupIdx == -1 {
		http.Error(w, "Group not found", http.StatusNotFound)
		return
	}

	found := false
	newServices := make([]Service, 0, len(state.groups[groupIdx].Services))
	for _, s := range state.groups[groupIdx].Services {
		if s.Name == payload.ServiceName {
			found = true
			continue
		}
		newServices = append(newServices, s)
	}

	if !found {
		http.Error(w, "Service not found", http.StatusNotFound)
		return
	}

	state.groups[groupIdx].Services = newServices

	if err := saveGroupsToFile(configFile, state.groups); err != nil {
		http.Error(w, fmt.Sprintf("Save error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Service deleted",
	})
}

// apiAdminMoveService — POST /api/admin/service/move
func apiAdminMoveService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		FromGroup string `json:"from_group"`
		ToGroup   string `json:"to_group"`
		Service   string `json:"service"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if err := validateName(payload.FromGroup, "From group"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateName(payload.ToGroup, "To group"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateName(payload.Service, "Service name"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	state.groupsMu.Lock()
	defer state.groupsMu.Unlock()

	fromIdx := -1
	for i, g := range state.groups {
		if strings.EqualFold(g.Name, payload.FromGroup) {
			fromIdx = i
			break
		}
	}

	if fromIdx == -1 {
		http.Error(w, "Source group not found", http.StatusNotFound)
		return
	}

	toIdx := -1
	for i, g := range state.groups {
		if strings.EqualFold(g.Name, payload.ToGroup) {
			toIdx = i
			break
		}
	}

	if toIdx == -1 {
		http.Error(w, "Target group not found", http.StatusNotFound)
		return
	}

	serviceIdx := -1
	for i, s := range state.groups[fromIdx].Services {
		if s.Name == payload.Service {
			serviceIdx = i
			break
		}
	}

	if serviceIdx == -1 {
		http.Error(w, "Service not found", http.StatusNotFound)
		return
	}

	svc := state.groups[fromIdx].Services[serviceIdx]
	state.groups[fromIdx].Services = append(
		state.groups[fromIdx].Services[:serviceIdx],
		state.groups[fromIdx].Services[serviceIdx+1:]...,
	)
	state.groups[toIdx].Services = append(state.groups[toIdx].Services, svc)

	if err := saveGroupsToFile(configFile, state.groups); err != nil {
		http.Error(w, fmt.Sprintf("Save error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Service moved",
	})
}

// apiAdminReorderServices — POST /api/admin/service/reorder
func apiAdminReorderServices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		GroupName string   `json:"group_name"`
		Services  []string `json:"services"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if err := validateName(payload.GroupName, "Group name"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if len(payload.Services) == 0 {
		http.Error(w, "Services list is empty", http.StatusBadRequest)
		return
	}

	// Validate each service name
	for _, svcName := range payload.Services {
		if err := validateName(svcName, "Service name"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	state.groupsMu.Lock()
	defer state.groupsMu.Unlock()

	groupIdx := -1
	for i, g := range state.groups {
		if strings.EqualFold(g.Name, payload.GroupName) {
			groupIdx = i
			break
		}
	}

	if groupIdx == -1 {
		http.Error(w, "Group not found", http.StatusNotFound)
		return
	}

	if len(payload.Services) != len(state.groups[groupIdx].Services) {
		http.Error(w, "Services count mismatch", http.StatusBadRequest)
		return
	}

	svcMap := make(map[string]Service, len(state.groups[groupIdx].Services))
	for _, s := range state.groups[groupIdx].Services {
		svcMap[s.Name] = s
	}

	newServices := make([]Service, 0, len(payload.Services))
	for _, name := range payload.Services {
		svc, ok := svcMap[name]
		if !ok {
			http.Error(w, fmt.Sprintf("Service %q not found", name), http.StatusBadRequest)
			return
		}
		newServices = append(newServices, svc)
	}

	state.groups[groupIdx].Services = newServices

	if err := saveGroupsToFile(configFile, state.groups); err != nil {
		http.Error(w, fmt.Sprintf("Save error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Services order updated",
	})
}
