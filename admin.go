package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ─── Validation ───

// nameRegex — допустимые символы в именах
var nameRegex = regexp.MustCompile(`^[a-zA-Zа-яА-ЯёЁ0-9 _\-.]+$`)

const maxNameLength = 100

func validateName(name, label string) error {
	if len(strings.TrimSpace(name)) == 0 {
		return fmt.Errorf("%s обязательно", label)
	}
	if utf8.RuneCountInString(name) > maxNameLength {
		return fmt.Errorf("%s должен быть не длиннее %d символов", label, maxNameLength)
	}
	if !nameRegex.MatchString(name) {
		return fmt.Errorf("%s содержит недопустимые символы (разрешены буквы, цифры, пробелы, дефисы, подчёркивания, точки)", label)
	}
	return nil
}

func validateURL(u, label string) error {
	if strings.TrimSpace(u) == "" {
		return nil
	}
	parsed, err := url.ParseRequestURI(u)
	if err != nil {
		return fmt.Errorf("%s не является корректным URL: %w", label, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%s должен использовать схему http или https", label)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%s должен содержать хост", label)
	}
	return nil
}

func validateIP(ip, label string) error {
	if strings.TrimSpace(ip) == "" {
		return nil
	}
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("%s не является корректным IP-адресом", label)
	}
	return nil
}

func normalizeService(s Service) Service {
	s.Name = strings.TrimSpace(s.Name)
	s.URL = strings.TrimSpace(s.URL)
	s.IP = strings.TrimSpace(s.IP)
	s.Icon = strings.TrimSpace(s.Icon)
	return s
}

func validateService(s Service) error {
	if err := validateName(s.Name, "Название сервиса"); err != nil {
		return err
	}
	if s.URL == "" && s.IP == "" {
		return fmt.Errorf("Необходимо указать URL или IP")
	}
	if err := validateURL(s.URL, "URL сервиса"); err != nil {
		return err
	}
	if err := validateIP(s.IP, "IP-адрес сервиса"); err != nil {
		return err
	}
	return nil
}

// ─── Admin helpers ───

type adminError struct {
	status int
	msg    string
}

func (e *adminError) Error() string { return e.msg }

func adminErr(status int, format string, args ...any) error {
	return &adminError{status: status, msg: fmt.Sprintf(format, args...)}
}

func writeAdminError(w http.ResponseWriter, err error) {
	var ae *adminError
	if errors.As(err, &ae) {
		http.Error(w, ae.msg, ae.status)
		return
	}
	http.Error(w, err.Error(), http.StatusBadRequest)
}

func writeAdminSuccess(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": message,
	}); err != nil {
		slog.Debug("Failed to encode admin response", "error", err)
	}
}

// mutateGroups serializes admin writes, applies the mutation to a private
// copy, saves it to disk, and only then swaps the in-memory state. A failed
// save leaves both disk and memory untouched.
func (app *App) mutateGroups(fn func([]Group) ([]Group, error)) error {
	app.adminMu.Lock()
	defer app.adminMu.Unlock()
	app.reloadMu.Lock()
	defer app.reloadMu.Unlock()

	app.State.mu.RLock()
	groups := cloneGroups(app.State.groups)
	app.State.mu.RUnlock()

	updated, err := fn(groups)
	if err != nil {
		return err
	}
	if err := app.SaveGroups(updated); err != nil {
		return adminErr(http.StatusInternalServerError, "Ошибка сохранения: %v", err)
	}
	app.SetGroups(updated)
	return nil
}

func findGroupIndex(groups []Group, name string) (int, bool) {
	for i, g := range groups {
		if strings.EqualFold(g.Name, name) {
			return i, true
		}
	}
	return -1, false
}

func findServiceIndex(services []Service, name string) (int, bool) {
	for i, s := range services {
		if strings.EqualFold(s.Name, name) {
			return i, true
		}
	}
	return -1, false
}

// serviceNameTaken reports whether a service with the same name exists,
// skipping the source service when updating or moving.
func serviceNameTaken(groups []Group, name string, skipGroup, skipService int) bool {
	for gi, g := range groups {
		for si, s := range g.Services {
			if gi == skipGroup && si == skipService {
				continue
			}
			if strings.EqualFold(s.Name, name) {
				return true
			}
		}
	}
	return false
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

func (app *App) apiAdminGroups(w http.ResponseWriter, _ *http.Request) {
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

	payload.Name = strings.TrimSpace(payload.Name)
	if err := validateName(payload.Name, "Название группы"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err := app.mutateGroups(func(groups []Group) ([]Group, error) {
		if _, ok := findGroupIndex(groups, payload.Name); ok {
			return nil, adminErr(http.StatusBadRequest, "Группа с таким именем уже существует")
		}
		return append(groups, Group{Name: payload.Name, Services: []Service{}}), nil
	})
	if err != nil {
		writeAdminError(w, err)
		return
	}
	writeAdminSuccess(w, "Группа добавлена")
}

func (app *App) apiAdminDeleteGroup(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Name string `json:"name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, fmt.Sprintf("Некорректный JSON: %v", err), http.StatusBadRequest)
		return
	}

	payload.Name = strings.TrimSpace(payload.Name)
	if err := validateName(payload.Name, "Название группы"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err := app.mutateGroups(func(groups []Group) ([]Group, error) {
		if len(groups) <= 1 {
			return nil, adminErr(http.StatusBadRequest, "Нельзя удалить последнюю группу")
		}
		newGroups := make([]Group, 0, len(groups))
		found := false
		for _, g := range groups {
			if strings.EqualFold(g.Name, payload.Name) {
				found = true
				continue
			}
			newGroups = append(newGroups, g)
		}
		if !found {
			return nil, adminErr(http.StatusNotFound, "Группа не найдена")
		}
		return newGroups, nil
	})
	if err != nil {
		writeAdminError(w, err)
		return
	}
	writeAdminSuccess(w, "Группа удалена")
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

	payload.OldName = strings.TrimSpace(payload.OldName)
	payload.NewName = strings.TrimSpace(payload.NewName)
	if err := validateName(payload.OldName, "Старое название группы"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateName(payload.NewName, "Новое название группы"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err := app.mutateGroups(func(groups []Group) ([]Group, error) {
		for _, g := range groups {
			if strings.EqualFold(g.Name, payload.NewName) && !strings.EqualFold(g.Name, payload.OldName) {
				return nil, adminErr(http.StatusBadRequest, "Группа с таким именем уже существует")
			}
		}
		idx, ok := findGroupIndex(groups, payload.OldName)
		if !ok {
			return nil, adminErr(http.StatusNotFound, "Группа не найдена")
		}
		groups[idx].Name = payload.NewName
		return groups, nil
	})
	if err != nil {
		writeAdminError(w, err)
		return
	}
	writeAdminSuccess(w, "Группа переименована")
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

	payload.GroupName = strings.TrimSpace(payload.GroupName)
	payload.Service = normalizeService(payload.Service)
	if err := validateName(payload.GroupName, "Название группы"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateService(payload.Service); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err := app.mutateGroups(func(groups []Group) ([]Group, error) {
		groupIdx, ok := findGroupIndex(groups, payload.GroupName)
		if !ok {
			return nil, adminErr(http.StatusNotFound, "Группа не найдена")
		}
		if serviceNameTaken(groups, payload.Service.Name, -1, -1) {
			return nil, adminErr(http.StatusBadRequest, "Сервис с таким именем уже существует")
		}
		groups[groupIdx].Services = append(groups[groupIdx].Services, payload.Service)
		return groups, nil
	})
	if err != nil {
		writeAdminError(w, err)
		return
	}
	writeAdminSuccess(w, "Сервис добавлен")
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

	payload.GroupName = strings.TrimSpace(payload.GroupName)
	payload.OldName = strings.TrimSpace(payload.OldName)
	payload.NewService = normalizeService(payload.NewService)

	if err := validateName(payload.GroupName, "Название группы"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateName(payload.OldName, "Старое название сервиса"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateService(payload.NewService); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err := app.mutateGroups(func(groups []Group) ([]Group, error) {
		groupIdx, ok := findGroupIndex(groups, payload.GroupName)
		if !ok {
			return nil, adminErr(http.StatusNotFound, "Группа не найдена")
		}
		serviceIdx, ok := findServiceIndex(groups[groupIdx].Services, payload.OldName)
		if !ok {
			return nil, adminErr(http.StatusNotFound, "Сервис не найден")
		}
		if serviceNameTaken(groups, payload.NewService.Name, groupIdx, serviceIdx) {
			return nil, adminErr(http.StatusBadRequest, "Сервис с таким именем уже существует")
		}
		// Preserve the existing icon if the client did not send one.
		if payload.NewService.Icon == "" {
			payload.NewService.Icon = groups[groupIdx].Services[serviceIdx].Icon
		}
		groups[groupIdx].Services[serviceIdx] = payload.NewService
		return groups, nil
	})
	if err != nil {
		writeAdminError(w, err)
		return
	}
	writeAdminSuccess(w, "Сервис обновлён")
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

	payload.GroupName = strings.TrimSpace(payload.GroupName)
	payload.ServiceName = strings.TrimSpace(payload.ServiceName)
	if err := validateName(payload.GroupName, "Название группы"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateName(payload.ServiceName, "Название сервиса"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err := app.mutateGroups(func(groups []Group) ([]Group, error) {
		groupIdx, ok := findGroupIndex(groups, payload.GroupName)
		if !ok {
			return nil, adminErr(http.StatusNotFound, "Группа не найдена")
		}

		newServices := make([]Service, 0, len(groups[groupIdx].Services))
		found := false
		for _, s := range groups[groupIdx].Services {
			if strings.EqualFold(s.Name, payload.ServiceName) {
				found = true
				continue
			}
			newServices = append(newServices, s)
		}
		if !found {
			return nil, adminErr(http.StatusNotFound, "Сервис не найден")
		}
		groups[groupIdx].Services = newServices
		return groups, nil
	})
	if err != nil {
		writeAdminError(w, err)
		return
	}
	writeAdminSuccess(w, "Сервис удалён")
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

	payload.FromGroup = strings.TrimSpace(payload.FromGroup)
	payload.ToGroup = strings.TrimSpace(payload.ToGroup)
	payload.Service = strings.TrimSpace(payload.Service)
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

	err := app.mutateGroups(func(groups []Group) ([]Group, error) {
		fromIdx, ok := findGroupIndex(groups, payload.FromGroup)
		if !ok {
			return nil, adminErr(http.StatusNotFound, "Исходная группа не найдена")
		}
		toIdx, ok := findGroupIndex(groups, payload.ToGroup)
		if !ok {
			return nil, adminErr(http.StatusNotFound, "Целевая группа не найдена")
		}
		if fromIdx == toIdx {
			return nil, adminErr(http.StatusBadRequest, "Исходная и целевая группы совпадают")
		}

		serviceIdx, ok := findServiceIndex(groups[fromIdx].Services, payload.Service)
		if !ok {
			return nil, adminErr(http.StatusNotFound, "Сервис не найден")
		}
		if serviceNameTaken(groups, payload.Service, fromIdx, serviceIdx) {
			return nil, adminErr(http.StatusBadRequest, "Сервис с таким именем уже существует в целевой группе")
		}

		svc := groups[fromIdx].Services[serviceIdx]
		groups[fromIdx].Services = append(
			groups[fromIdx].Services[:serviceIdx],
			groups[fromIdx].Services[serviceIdx+1:]...,
		)
		groups[toIdx].Services = append(groups[toIdx].Services, svc)
		return groups, nil
	})
	if err != nil {
		writeAdminError(w, err)
		return
	}
	writeAdminSuccess(w, "Сервис перемещён")
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

	payload.GroupName = strings.TrimSpace(payload.GroupName)
	if err := validateName(payload.GroupName, "Название группы"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(payload.Services) == 0 {
		http.Error(w, "Список сервисов пуст", http.StatusBadRequest)
		return
	}
	for i := range payload.Services {
		payload.Services[i] = strings.TrimSpace(payload.Services[i])
		if err := validateName(payload.Services[i], "Название сервиса"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	err := app.mutateGroups(func(groups []Group) ([]Group, error) {
		groupIdx, ok := findGroupIndex(groups, payload.GroupName)
		if !ok {
			return nil, adminErr(http.StatusNotFound, "Группа не найдена")
		}

		current := groups[groupIdx].Services
		if len(payload.Services) != len(current) {
			return nil, adminErr(http.StatusBadRequest, "Количество сервисов не совпадает")
		}

		byName := make(map[string]Service, len(current))
		for _, s := range current {
			byName[strings.ToLower(s.Name)] = s
		}

		newServices := make([]Service, 0, len(payload.Services))
		seen := make(map[string]bool, len(payload.Services))
		for _, name := range payload.Services {
			key := strings.ToLower(name)
			if seen[key] {
				return nil, adminErr(http.StatusBadRequest, "Список сервисов содержит дубликаты: %q", name)
			}
			seen[key] = true
			svc, ok := byName[key]
			if !ok {
				return nil, adminErr(http.StatusBadRequest, "Сервис %q не найден", name)
			}
			newServices = append(newServices, svc)
		}

		groups[groupIdx].Services = newServices
		return groups, nil
	})
	if err != nil {
		writeAdminError(w, err)
		return
	}
	writeAdminSuccess(w, "Порядок сервисов обновлён")
}
