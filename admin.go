package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// ─── Validation ───

// nameRegex — допустимые символы в именах
var nameRegex = regexp.MustCompile(`^[a-zA-Zа-яА-ЯёЁ0-9 _\-.]+$`)

const maxNameLength = 100

func validateName(name, label string) error {
	if len(strings.TrimSpace(name)) == 0 {
		return fmt.Errorf("%s обязательно", label)
	}
	if len(name) > maxNameLength {
		return fmt.Errorf("%s должен быть не длиннее %d символов", label, maxNameLength)
	}
	if !nameRegex.MatchString(name) {
		return fmt.Errorf("%s содержит недопустимые символы (разрешены буквы, цифры, пробелы, дефисы, подчёркивания, точки)", label)
	}
	return nil
}

func validateURL(u, label string) error {
	if u == "" {
		return nil
	}
	parsed, err := url.ParseRequestURI(u)
	if err != nil {
		return fmt.Errorf("%s не является корректным URL: %w", label, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%s должен использовать схему http или https", label)
	}
	return nil
}

func validateIP(ip, label string) error {
	if ip == "" {
		return nil
	}
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("%s не является корректным IP-адресом", label)
	}
	return nil
}

// ─── Admin Middleware ───

// adminAuthMiddleware — API key check for admin endpoints
func (app *App) adminAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		if !app.RequireAdminAuth.Load() {
			next.ServeHTTP(w, r)
			return
		}

		if app.AdminAPIKey == "" {
			http.Error(w, "Панель администратора отключена. Установите переменную окружения ADMIN_API_KEY для включения.", http.StatusForbidden)
			return
		}

		auth := r.Header.Get("Authorization")
		if auth == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="admin"`)
			http.Error(w, "Требуется авторизация", http.StatusUnauthorized)
			return
		}

		auth = strings.TrimPrefix(auth, "Bearer ")

		if auth != app.AdminAPIKey {
			http.Error(w, "Неверный API-ключ", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// ─── Admin Page Handler ───

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
		http.Error(w, "Внутренняя ошибка сервера", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := app.AdminTmpl.ExecuteTemplate(w, "admin.html", AdminData{
		Groups:     groups,
		GroupsJSON: template.JS(groupsJSON),
	}); err != nil {
		slog.Error("Admin template rendering error", "groups_count", len(groups), "error", err)
		http.Error(w, "Внутренняя ошибка сервера", http.StatusInternalServerError)
	}
}

// ─── Admin API: Groups ───

func (app *App) apiAdminGroups(w http.ResponseWriter, r *http.Request) {
	groups := app.GetGroupsCopy()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"groups": groups,
	}); err != nil {
		slog.Debug("Failed to encode groups", "error", err)
	}
}

func (app *App) apiAdminAddGroup(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Name string `json:"name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, fmt.Sprintf("Некорректный JSON: %v", err), http.StatusBadRequest)
		return
	}

	if err := validateName(payload.Name, "Название группы"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	app.State.mu.Lock()

	for _, g := range app.State.groups {
		if strings.EqualFold(g.Name, payload.Name) {
			app.State.mu.Unlock()
			http.Error(w, "Группа с таким именем уже существует", http.StatusBadRequest)
			return
		}
	}

	app.State.groups = append(app.State.groups, Group{
		Name:     payload.Name,
		Services: []Service{},
	})
	groupsCopy := app.cloneGroupsLocked()
	app.State.mu.Unlock()

	if err := app.SaveGroups(groupsCopy); err != nil {
		http.Error(w, fmt.Sprintf("Ошибка сохранения: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Группа добавлена",
	}); err != nil {
		slog.Debug("Failed to encode add group response", "error", err)
	}
}

func (app *App) apiAdminDeleteGroup(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Name string `json:"name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, fmt.Sprintf("Некорректный JSON: %v", err), http.StatusBadRequest)
		return
	}

	if err := validateName(payload.Name, "Название группы"); err != nil {
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
		http.Error(w, "Группа не найдена", http.StatusNotFound)
		return
	}

	app.State.groups = newGroups
	groupsCopy := app.cloneGroupsLocked()
	app.State.mu.Unlock()

	if err := app.SaveGroups(groupsCopy); err != nil {
		http.Error(w, fmt.Sprintf("Ошибка сохранения: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Группа удалена",
	}); err != nil {
		slog.Debug("Failed to encode delete group response", "error", err)
	}
}

func (app *App) apiAdminRenameGroup(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		OldName string `json:"old_name"`
		NewName string `json:"new_name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, fmt.Sprintf("Некорректный JSON: %v", err), http.StatusBadRequest)
		return
	}

	if err := validateName(payload.OldName, "Старое название группы"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateName(payload.NewName, "Новое название группы"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	app.State.mu.Lock()

	for _, g := range app.State.groups {
		if strings.EqualFold(g.Name, payload.NewName) && !strings.EqualFold(g.Name, payload.OldName) {
			app.State.mu.Unlock()
			http.Error(w, "Группа с таким именем уже существует", http.StatusBadRequest)
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
		http.Error(w, "Группа не найдена", http.StatusNotFound)
		return
	}

	groupsCopy := app.cloneGroupsLocked()
	app.State.mu.Unlock()

	if err := app.SaveGroups(groupsCopy); err != nil {
		http.Error(w, fmt.Sprintf("Ошибка сохранения: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Группа переименована",
	}); err != nil {
		slog.Debug("Failed to encode rename group response", "error", err)
	}
}

// ─── Admin API: Services ───

func (app *App) apiAdminAddService(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		GroupName string  `json:"group_name"`
		Service   Service `json:"service"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, fmt.Sprintf("Некорректный JSON: %v", err), http.StatusBadRequest)
		return
	}

	if err := validateName(payload.GroupName, "Название группы"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateName(payload.Service.Name, "Название сервиса"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(payload.Service.URL) == "" && strings.TrimSpace(payload.Service.IP) == "" {
		http.Error(w, "Необходимо указать URL или IP", http.StatusBadRequest)
		return
	}
	if err := validateURL(payload.Service.URL, "URL сервиса"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateIP(payload.Service.IP, "IP-адрес сервиса"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	app.State.mu.Lock()

	groupIdx, ok := app.findGroupIndexLocked(payload.GroupName)
	if !ok {
		app.State.mu.Unlock()
		http.Error(w, "Группа не найдена", http.StatusNotFound)
		return
	}

	for _, s := range app.State.groups[groupIdx].Services {
		if strings.EqualFold(s.Name, payload.Service.Name) {
			app.State.mu.Unlock()
			http.Error(w, "Сервис с таким именем уже существует в группе", http.StatusBadRequest)
			return
		}
	}

	app.State.groups[groupIdx].Services = append(app.State.groups[groupIdx].Services, payload.Service)
	groupsCopy := app.cloneGroupsLocked()
	app.State.mu.Unlock()

	if err := app.SaveGroups(groupsCopy); err != nil {
		http.Error(w, fmt.Sprintf("Ошибка сохранения: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Сервис добавлен",
	}); err != nil {
		slog.Debug("Failed to encode add service response", "error", err)
	}
}

func (app *App) apiAdminUpdateService(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		GroupName  string  `json:"group_name"`
		OldName    string  `json:"old_name"`
		NewService Service `json:"new_service"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, fmt.Sprintf("Некорректный JSON: %v", err), http.StatusBadRequest)
		return
	}

	if err := validateName(payload.GroupName, "Название группы"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateName(payload.OldName, "Старое название сервиса"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateName(payload.NewService.Name, "Новое название сервиса"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateURL(payload.NewService.URL, "URL сервиса"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateIP(payload.NewService.IP, "IP-адрес сервиса"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	app.State.mu.Lock()

	groupIdx, ok := app.findGroupIndexLocked(payload.GroupName)
	if !ok {
		app.State.mu.Unlock()
		http.Error(w, "Группа не найдена", http.StatusNotFound)
		return
	}

	serviceIdx, ok := app.findServiceIndexLocked(app.State.groups[groupIdx].Services, payload.OldName)
	if !ok {
		app.State.mu.Unlock()
		http.Error(w, "Сервис не найден", http.StatusNotFound)
		return
	}

	app.State.groups[groupIdx].Services[serviceIdx] = payload.NewService
	groupsCopy := app.cloneGroupsLocked()
	app.State.mu.Unlock()

	if err := app.SaveGroups(groupsCopy); err != nil {
		http.Error(w, fmt.Sprintf("Ошибка сохранения: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Сервис обновлён",
	}); err != nil {
		slog.Debug("Failed to encode update service response", "error", err)
	}
}

func (app *App) apiAdminDeleteService(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		GroupName   string `json:"group_name"`
		ServiceName string `json:"service_name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, fmt.Sprintf("Некорректный JSON: %v", err), http.StatusBadRequest)
		return
	}

	if err := validateName(payload.GroupName, "Название группы"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateName(payload.ServiceName, "Название сервиса"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	app.State.mu.Lock()

	groupIdx, ok := app.findGroupIndexLocked(payload.GroupName)
	if !ok {
		app.State.mu.Unlock()
		http.Error(w, "Группа не найдена", http.StatusNotFound)
		return
	}

	found := false
	newServices := make([]Service, 0, len(app.State.groups[groupIdx].Services))
	for _, s := range app.State.groups[groupIdx].Services {
		if strings.EqualFold(s.Name, payload.ServiceName) {
			found = true
			continue
		}
		newServices = append(newServices, s)
	}

	if !found {
		app.State.mu.Unlock()
		http.Error(w, "Сервис не найден", http.StatusNotFound)
		return
	}

	app.State.groups[groupIdx].Services = newServices
	groupsCopy := app.cloneGroupsLocked()
	app.State.mu.Unlock()

	if err := app.SaveGroups(groupsCopy); err != nil {
		http.Error(w, fmt.Sprintf("Ошибка сохранения: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Сервис удалён",
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
		http.Error(w, fmt.Sprintf("Некорректный JSON: %v", err), http.StatusBadRequest)
		return
	}

	if err := validateName(payload.FromGroup, "Исходная группа"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateName(payload.ToGroup, "Целевая группа"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateName(payload.Service, "Название сервиса"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	app.State.mu.Lock()

	fromIdx, ok := app.findGroupIndexLocked(payload.FromGroup)
	if !ok {
		app.State.mu.Unlock()
		http.Error(w, "Исходная группа не найдена", http.StatusNotFound)
		return
	}

	toIdx, ok := app.findGroupIndexLocked(payload.ToGroup)
	if !ok {
		app.State.mu.Unlock()
		http.Error(w, "Целевая группа не найдена", http.StatusNotFound)
		return
	}

	serviceIdx, ok := app.findServiceIndexLocked(app.State.groups[fromIdx].Services, payload.Service)
	if !ok {
		app.State.mu.Unlock()
		http.Error(w, "Сервис не найден", http.StatusNotFound)
		return
	}

	svc := app.State.groups[fromIdx].Services[serviceIdx]
	app.State.groups[fromIdx].Services = append(
		app.State.groups[fromIdx].Services[:serviceIdx],
		app.State.groups[fromIdx].Services[serviceIdx+1:]...,
	)
	app.State.groups[toIdx].Services = append(app.State.groups[toIdx].Services, svc)

	groupsCopy := app.cloneGroupsLocked()
	app.State.mu.Unlock()

	if err := app.SaveGroups(groupsCopy); err != nil {
		http.Error(w, fmt.Sprintf("Ошибка сохранения: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Сервис перемещён",
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
		http.Error(w, fmt.Sprintf("Некорректный JSON: %v", err), http.StatusBadRequest)
		return
	}

	if err := validateName(payload.GroupName, "Название группы"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(payload.Services) == 0 {
		http.Error(w, "Список сервисов пуст", http.StatusBadRequest)
		return
	}
	for _, svcName := range payload.Services {
		if err := validateName(svcName, "Название сервиса"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	app.State.mu.Lock()

	groupIdx, ok := app.findGroupIndexLocked(payload.GroupName)
	if !ok {
		app.State.mu.Unlock()
		http.Error(w, "Группа не найдена", http.StatusNotFound)
		return
	}

	if len(payload.Services) != len(app.State.groups[groupIdx].Services) {
		app.State.mu.Unlock()
		http.Error(w, "Количество сервисов не совпадает", http.StatusBadRequest)
		return
	}

	svcMap := make(map[string]Service, len(app.State.groups[groupIdx].Services))
	for _, s := range app.State.groups[groupIdx].Services {
		svcMap[strings.ToLower(s.Name)] = s
	}

	newServices := make([]Service, 0, len(payload.Services))
	for _, name := range payload.Services {
		svc, ok := svcMap[strings.ToLower(name)]
		if !ok {
			app.State.mu.Unlock()
			http.Error(w, fmt.Sprintf("Сервис %q не найден", name), http.StatusBadRequest)
			return
		}
		newServices = append(newServices, svc)
	}

	app.State.groups[groupIdx].Services = newServices
	groupsCopy := app.cloneGroupsLocked()
	app.State.mu.Unlock()

	if err := app.SaveGroups(groupsCopy); err != nil {
		http.Error(w, fmt.Sprintf("Ошибка сохранения: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Порядок сервисов обновлён",
	}); err != nil {
		slog.Debug("Failed to encode reorder service response", "error", err)
	}
}

// ─── Locked helpers (used only by admin handlers) ───

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

func (app *App) findGroupIndexLocked(name string) (int, bool) {
	for i, g := range app.State.groups {
		if strings.EqualFold(g.Name, name) {
			return i, true
		}
	}
	return -1, false
}

func (app *App) findServiceIndexLocked(services []Service, name string) (int, bool) {
	for i, s := range services {
		if strings.EqualFold(s.Name, name) {
			return i, true
		}
	}
	return -1, false
}
