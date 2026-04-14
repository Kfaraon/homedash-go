package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ─── Validation ───

// nameValidation — допустимые символы в именах (буквы, цифры, пробелы, дефисы, подчёркивания, точки)
//
//nolint:gochecknoglobals
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
	parsed, err := url.ParseRequestURI(u)
	if err != nil {
		return fmt.Errorf("%s is not a valid URL: %w", label, err)
	}
	// Проверка на наличие схемы
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%s must use http or https scheme", label)
	}
	return nil
}

// validateIP checks if the string is a valid IPv4 or IPv6 address
func validateIP(ip, label string) error {
	if ip == "" {
		return nil // IP is optional
	}
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("%s is not a valid IP address", label)
	}
	return nil
}

// ─── Main handlers ───

// ServeHome — handler for the main page
func (app *App) ServeHome(w http.ResponseWriter, _ *http.Request) {
	groups := app.GetGroupsCopy()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := app.HomeTmpl.ExecuteTemplate(w, "home.html", map[string]any{"groups": groups}); err != nil {
		slog.Error("Template rendering error", "groups_count", len(groups), "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// ServeStatus — handler for status API
func (app *App) ServeStatus(w http.ResponseWriter, r *http.Request) {
	groups := app.GetGroupsCopy()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(app.getCachedStatuses(groups)); err != nil {
		slog.Debug("Failed to encode status response", "error", err)
	}
}

// ServeHealth — handler for health check
func (app *App) ServeHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Check config file
	configOK := true
	if _, err := app.LoadGroups(); err != nil {
		slog.Warn("Health check: config load failed", "error", err)
		configOK = false
	}

	status := "ok"
	if !configOK {
		status = "degraded"
	}

	if err := json.NewEncoder(w).Encode(map[string]any{
		"status":        status,
		"config_ok":     configOK,
		"cache_entries": app.GetCacheCount(),
		"groups_count":  app.GetGroupsCount(),
		"timestamp":     time.Now().Format(time.RFC3339),
	}); err != nil {
		slog.Debug("Failed to encode health check", "error", err)
	}
}

// ServeMetrics — handler for metrics (JSON format for frontend)
func (app *App) ServeMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(app.Metrics.GetSnapshot()); err != nil {
		slog.Debug("Failed to encode metrics", "error", err)
	}
}

// ServePrometheusMetrics — handler for Prometheus metrics (text format)
func (app *App) ServePrometheusMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if _, err := w.Write([]byte(app.Metrics.GetPrometheusMetrics())); err != nil {
		slog.Debug("Failed to write prometheus metrics", "error", err)
	}
}

// ─── Middleware ───

// corsMiddleware — middleware for CORS
func (app *App) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		if app.AllowedOrigins == "" {
			// Allow all by default (backward compatibility)
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else if origin != "" {
			// Check specific origin
			for _, ao := range strings.Split(app.AllowedOrigins, ",") {
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
func (app *App) adminAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth for CORS preflight
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		// If auth is disabled, skip authentication
		if !app.RequireAdminAuth.Load() {
			next.ServeHTTP(w, r)
			return
		}

		if app.AdminAPIKey == "" {
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
		auth = strings.TrimPrefix(auth, "Bearer ")

		if auth != app.AdminAPIKey {
			http.Error(w, "Invalid API key", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// maxBytesMiddleware — request body size limit (1MB)
func (app *App) maxBytesMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB
		}
		next.ServeHTTP(w, r)
	})
}

// contentTypeMiddleware — Content-Type check for POST/PUT requests
func (app *App) contentTypeMiddleware(next http.Handler) http.Handler {
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

// ─── Cached Statuses with stale-while-revalidate ───

func (app *App) getCachedStatuses(groups []Group) map[string]any {
	// Check main cache validity
	if cache := app.GetCache(); cache != nil {
		app.Metrics.IncrementCacheHits()
		return app.statusResp(cache)
	}

	// Stale-while-revalidate: return stale cache while refreshing
	if stale, ok := app.GetStaleCache(); ok {
		app.Metrics.IncrementCacheHits()
		// Background refresh
		go app.refreshCache(context.Background(), groups)
		return app.statusResp(stale)
	}

	// No cache at all — return empty result and refresh in background
	// Never block the HTTP handler
	app.Metrics.IncrementCacheMisses()
	go app.refreshCache(context.Background(), groups)
	return app.statusResp(make(map[string]Status))
}

// refreshCache background cache refresh
func (app *App) refreshCache(ctx context.Context, groups []Group) {
	checkCtx, cancel := context.WithTimeout(ctx, time.Duration(len(groups)*2)*time.Second)
	defer cancel()

	sm := checkServicesInParallel(checkCtx, groups, app.Metrics, app.PingTimeout)
	app.SetCache(sm)
}

func (app *App) statusResp(services map[string]Status) map[string]any {
	return map[string]any{
		"services":  services,
		"timestamp": time.Now().Format(time.RFC3339),
	}
}

// ─── Admin handlers ───

// ServeAdmin — displays admin page
func (app *App) ServeAdmin(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/admin" {
		http.NotFound(w, r)
		return
	}

	groups := app.GetGroupsCopy()
	groupsJSON, err := json.Marshal(groups)
	if err != nil {
		slog.Error("Error marshaling admin groups", "error", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := app.AdminTmpl.ExecuteTemplate(w, "admin.html", AdminData{
		Groups:     groups,
		GroupsJSON: template.JS(groupsJSON),
	}); err != nil {
		slog.Error("Admin template rendering error", "groups_count", len(groups), "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// ─── Admin API: Groups ───

// apiAdminGroups — GET /api/admin/groups
func (app *App) apiAdminGroups(w http.ResponseWriter, r *http.Request) {
	groups := app.GetGroupsCopy()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"groups": groups,
	}); err != nil {
		slog.Debug("Failed to encode groups", "error", err)
	}
}

// apiAdminAddGroup — POST /api/admin/group
func (app *App) apiAdminAddGroup(w http.ResponseWriter, r *http.Request) {
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

	app.State.mu.Lock()

	for _, g := range app.State.groups {
		if strings.EqualFold(g.Name, payload.Name) {
			app.State.mu.Unlock()
			http.Error(w, "Group with this name already exists", http.StatusBadRequest)
			return
		}
	}

	app.State.groups = append(app.State.groups, Group{
		Name:     payload.Name,
		Services: []Service{},
	})
	// Copy before disk I/O to release lock quickly
	groupsCopy := app.cloneGroupsLocked()
	app.State.mu.Unlock()

	if err := app.SaveGroups(groupsCopy); err != nil {
		http.Error(w, fmt.Sprintf("Save error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Group added",
	}); err != nil {
		slog.Debug("Failed to encode add group response", "error", err)
	}
}

// apiAdminDeleteGroup — DELETE /api/admin/group
func (app *App) apiAdminDeleteGroup(w http.ResponseWriter, r *http.Request) {
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

	app.State.mu.Lock()

	found := false
	newGroups := make([]Group, 0, len(app.State.groups))
	for _, g := range app.State.groups {
		if strings.EqualFold(g.Name, payload.Name) {
			found = true
			continue
		}
		newGroups = append(newGroups, g)
	}

	if !found {
		app.State.mu.Unlock()
		http.Error(w, "Group not found", http.StatusNotFound)
		return
	}

	app.State.groups = newGroups
	// Copy before disk I/O
	groupsCopy := app.cloneGroupsLocked()
	app.State.mu.Unlock()

	if err := app.SaveGroups(groupsCopy); err != nil {
		http.Error(w, fmt.Sprintf("Save error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Group deleted",
	}); err != nil {
		slog.Debug("Failed to encode delete group response", "error", err)
	}
}

// apiAdminRenameGroup — PUT /api/admin/group
func (app *App) apiAdminRenameGroup(w http.ResponseWriter, r *http.Request) {
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

	app.State.mu.Lock()

	for _, g := range app.State.groups {
		if strings.EqualFold(g.Name, payload.NewName) && !strings.EqualFold(g.Name, payload.OldName) {
			app.State.mu.Unlock()
			http.Error(w, "Group with this name already exists", http.StatusBadRequest)
			return
		}
	}

	found := false
	for i, g := range app.State.groups {
		if strings.EqualFold(g.Name, payload.OldName) {
			app.State.groups[i].Name = payload.NewName
			found = true
			break
		}
	}

	if !found {
		app.State.mu.Unlock()
		http.Error(w, "Group not found", http.StatusNotFound)
		return
	}

	// Copy before disk I/O
	groupsCopy := app.cloneGroupsLocked()
	app.State.mu.Unlock()

	if err := app.SaveGroups(groupsCopy); err != nil {
		http.Error(w, fmt.Sprintf("Save error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Group renamed",
	}); err != nil {
		slog.Debug("Failed to encode rename group response", "error", err)
	}
}

// ─── Admin API: Services ───

// apiAdminAddService — POST /api/admin/service
func (app *App) apiAdminAddService(w http.ResponseWriter, r *http.Request) {
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
	if err := validateIP(payload.Service.IP, "Service IP"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	app.State.mu.Lock()

	groupIdx, ok := app.findGroupIndexLocked(payload.GroupName)
	if !ok {
		app.State.mu.Unlock()
		http.Error(w, "Group not found", http.StatusNotFound)
		return
	}

	app.State.groups[groupIdx].Services = append(app.State.groups[groupIdx].Services, payload.Service)
	// Copy before disk I/O
	groupsCopy := app.cloneGroupsLocked()
	app.State.mu.Unlock()

	if err := app.SaveGroups(groupsCopy); err != nil {
		http.Error(w, fmt.Sprintf("Save error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Service added",
	}); err != nil {
		slog.Debug("Failed to encode add service response", "error", err)
	}
}

// apiAdminUpdateService — PUT /api/admin/service
func (app *App) apiAdminUpdateService(w http.ResponseWriter, r *http.Request) {
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
	if err := validateIP(payload.NewService.IP, "Service IP"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	app.State.mu.Lock()

	groupIdx, ok := app.findGroupIndexLocked(payload.GroupName)
	if !ok {
		app.State.mu.Unlock()
		http.Error(w, "Group not found", http.StatusNotFound)
		return
	}

	serviceIdx, ok := app.findServiceIndexLocked(app.State.groups[groupIdx].Services, payload.OldName)
	if !ok {
		app.State.mu.Unlock()
		http.Error(w, "Service not found", http.StatusNotFound)
		return
	}

	app.State.groups[groupIdx].Services[serviceIdx] = payload.NewService
	// Copy before disk I/O
	groupsCopy := app.cloneGroupsLocked()
	app.State.mu.Unlock()

	if err := app.SaveGroups(groupsCopy); err != nil {
		http.Error(w, fmt.Sprintf("Save error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Service updated",
	}); err != nil {
		slog.Debug("Failed to encode update service response", "error", err)
	}
}

// apiAdminDeleteService — DELETE /api/admin/service
func (app *App) apiAdminDeleteService(w http.ResponseWriter, r *http.Request) {
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

	app.State.mu.Lock()

	groupIdx, ok := app.findGroupIndexLocked(payload.GroupName)
	if !ok {
		app.State.mu.Unlock()
		http.Error(w, "Group not found", http.StatusNotFound)
		return
	}

	found := false
	newServices := make([]Service, 0, len(app.State.groups[groupIdx].Services))
	for _, s := range app.State.groups[groupIdx].Services {
		if s.Name == payload.ServiceName {
			found = true
			continue
		}
		newServices = append(newServices, s)
	}

	if !found {
		app.State.mu.Unlock()
		http.Error(w, "Service not found", http.StatusNotFound)
		return
	}

	app.State.groups[groupIdx].Services = newServices
	// Copy before disk I/O
	groupsCopy := app.cloneGroupsLocked()
	app.State.mu.Unlock()

	if err := app.SaveGroups(groupsCopy); err != nil {
		http.Error(w, fmt.Sprintf("Save error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Service deleted",
	}); err != nil {
		slog.Debug("Failed to encode delete service response", "error", err)
	}
}

// apiAdminMoveService — POST /api/admin/service/move
func (app *App) apiAdminMoveService(w http.ResponseWriter, r *http.Request) {
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

	app.State.mu.Lock()

	fromIdx, ok := app.findGroupIndexLocked(payload.FromGroup)
	if !ok {
		app.State.mu.Unlock()
		http.Error(w, "Source group not found", http.StatusNotFound)
		return
	}

	toIdx, ok := app.findGroupIndexLocked(payload.ToGroup)
	if !ok {
		app.State.mu.Unlock()
		http.Error(w, "Target group not found", http.StatusNotFound)
		return
	}

	serviceIdx, ok := app.findServiceIndexLocked(app.State.groups[fromIdx].Services, payload.Service)
	if !ok {
		app.State.mu.Unlock()
		http.Error(w, "Service not found", http.StatusNotFound)
		return
	}

	svc := app.State.groups[fromIdx].Services[serviceIdx]
	app.State.groups[fromIdx].Services = append(
		app.State.groups[fromIdx].Services[:serviceIdx],
		app.State.groups[fromIdx].Services[serviceIdx+1:]...,
	)
	app.State.groups[toIdx].Services = append(app.State.groups[toIdx].Services, svc)

	// Copy before disk I/O
	groupsCopy := app.cloneGroupsLocked()
	app.State.mu.Unlock()

	if err := app.SaveGroups(groupsCopy); err != nil {
		http.Error(w, fmt.Sprintf("Save error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Service moved",
	}); err != nil {
		slog.Debug("Failed to encode move service response", "error", err)
	}
}

// apiAdminReorderServices — POST /api/admin/service/reorder
func (app *App) apiAdminReorderServices(w http.ResponseWriter, r *http.Request) {
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
	for _, svcName := range payload.Services {
		if err := validateName(svcName, "Service name"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	app.State.mu.Lock()

	groupIdx, ok := app.findGroupIndexLocked(payload.GroupName)
	if !ok {
		app.State.mu.Unlock()
		http.Error(w, "Group not found", http.StatusNotFound)
		return
	}

	if len(payload.Services) != len(app.State.groups[groupIdx].Services) {
		app.State.mu.Unlock()
		http.Error(w, "Services count mismatch", http.StatusBadRequest)
		return
	}

	svcMap := make(map[string]Service, len(app.State.groups[groupIdx].Services))
	for _, s := range app.State.groups[groupIdx].Services {
		svcMap[s.Name] = s
	}

	newServices := make([]Service, 0, len(payload.Services))
	for _, name := range payload.Services {
		svc, ok := svcMap[name]
		if !ok {
			app.State.mu.Unlock()
			http.Error(w, fmt.Sprintf("Service %q not found", name), http.StatusBadRequest)
			return
		}
		newServices = append(newServices, svc)
	}

	app.State.groups[groupIdx].Services = newServices
	// Copy before disk I/O
	groupsCopy := app.cloneGroupsLocked()
	app.State.mu.Unlock()

	if err := app.SaveGroups(groupsCopy); err != nil {
		http.Error(w, fmt.Sprintf("Save error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Services order updated",
	}); err != nil {
		slog.Debug("Failed to encode reorder service response", "error", err)
	}
}

// ─── Locked helpers ───

// cloneGroupsLocked creates a deep copy of current groups — MUST be called with state.mu held
func (app *App) cloneGroupsLocked() []Group {
	groupsCopy := make([]Group, len(app.State.groups))
	for i, g := range app.State.groups {
		groupsCopy[i] = Group{
			Name:     g.Name,
			Services: make([]Service, len(g.Services)),
		}
		copy(groupsCopy[i].Services, g.Services)
	}
	return groupsCopy
}

// findGroupIndexLocked finds group index — MUST be called with state.mu held
func (app *App) findGroupIndexLocked(name string) (int, bool) {
	for i, g := range app.State.groups {
		if strings.EqualFold(g.Name, name) {
			return i, true
		}
	}
	return -1, false
}

// findServiceIndexLocked finds service index — works on the passed slice
func (app *App) findServiceIndexLocked(services []Service, name string) (int, bool) {
	for i, s := range services {
		if strings.EqualFold(s.Name, name) {
			return i, true
		}
	}
	return -1, false
}
