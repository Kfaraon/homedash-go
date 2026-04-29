package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ─── Main handlers ───

// ServeHome — handler for the main page
func (app *App) ServeHome(w http.ResponseWriter, _ *http.Request) {
	groups := app.GetGroupsCopy()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := app.HomeTmpl.ExecuteTemplate(w, "home.html", map[string]any{"groups": groups}); err != nil {
		slog.Error("Template rendering error", "groups_count", len(groups), "error", err)
		http.Error(w, "Внутренняя ошибка сервера", http.StatusInternalServerError)
	}
}

// ServeStatus — handler for status API
func (app *App) ServeStatus(w http.ResponseWriter, r *http.Request) {
	groups := app.GetGroupsCopy()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(func() map[string]any {
		var _ []Group = groups
		return app.getCachedStatuses()
	}()); err != nil {
		slog.Debug("Failed to encode status response", "error", err)
	}
}

// ServeHealth — handler for health check
func (app *App) ServeHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

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

// ServeMyIP — returns cached or fresh public IP address
func (app *App) ServeMyIP(w http.ResponseWriter, r *http.Request) {
	if cached := app.getCachedIP(); cached != "" {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-IP-Source", "cache")
		w.Write([]byte(cached))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	ip, ipType, provider := app.fetchPublicIP(ctx)
	if ip == "" {
		app.Metrics.IncrementIPFetchErrors()
		http.Error(w, "Не удалось определить публичный IP-адрес", http.StatusBadGateway)
		return
	}

	app.updateIPCache(ip, ipType, provider)
	app.Metrics.IncrementIPFetches()

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("X-IP-Source", "fresh")
	w.Header().Set("X-IP-Provider", provider)
	w.Write([]byte(ip))
}

func (app *App) getCachedIP() string {
	app.IPCacheMu.RLock()
	defer app.IPCacheMu.RUnlock()

	if app.IPCache.IP != "" && time.Since(app.IPCache.FetchedAt) < app.IPCacheTTL {
		return app.IPCache.IP
	}
	return ""
}

func (app *App) updateIPCache(ip, ipType, provider string) {
	app.IPCacheMu.Lock()
	defer app.IPCacheMu.Unlock()

	app.IPCache = &IPCache{
		IP:        ip,
		Type:      ipType,
		Provider:  provider,
		FetchedAt: time.Now(),
	}
}

func (app *App) fetchPublicIP(ctx context.Context) (ip, ipType, provider string) {
	client := getHTTPClient(true)

	for _, providerURL := range app.IPProviders {
		select {
		case <-ctx.Done():
			return "", "", ""
		default:
		}

		req, err := http.NewRequestWithContext(ctx, "GET", providerURL, nil)
		if err != nil {
			slog.Debug("Failed to create IP request", "provider", providerURL, "error", err)
			continue
		}
		req.Header.Set("User-Agent", "homedash-go/1.0")
		req.Header.Set("Accept", "text/plain")

		resp, err := client.Do(req)
		if err != nil {
			slog.Debug("IP provider failed", "provider", providerURL, "error", err)
			continue
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()

		if err != nil || resp.StatusCode != http.StatusOK {
			continue
		}

		ip = strings.TrimSpace(string(body))
		if net.ParseIP(ip) == nil {
			continue
		}

		if strings.Contains(ip, ":") {
			ipType = "ipv6"
		} else {
			ipType = "ipv4"
		}

		if u, err := url.Parse(providerURL); err == nil {
			provider = u.Host
		} else {
			provider = providerURL
		}

		slog.Debug("Public IP fetched", "ip", ip, "type", ipType, "provider", provider)
		return ip, ipType, provider
	}

	return "", "", ""
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
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else if origin != "" {
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
				http.Error(w, "Content-Type должен быть application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ─── Cached Statuses ───

func (app *App) getCachedStatuses() map[string]any {
	if cache := app.GetCache(); cache != nil {
		app.Metrics.IncrementCacheHits()
		return app.statusResp(cache)
	}

	if stale, ok := app.GetStaleCache(); ok {
		app.Metrics.IncrementCacheHits()
		go app.refreshCacheIfNeeded()
		return app.statusResp(stale)
	}

	app.Metrics.IncrementCacheMisses()
	go app.refreshCacheIfNeeded()
	return app.statusResp(make(map[string]Status))
}

func (app *App) statusResp(services map[string]Status) map[string]any {
	return map[string]any{
		"services":  services,
		"timestamp": time.Now().Format(time.RFC3339),
	}
}

// ─── Icon Helpers (delegates to iconResolver) ───

func (app *App) ResolveIcon(name, explicitIcon string) string {
	return app.iconResolver.ResolveIcon(name, explicitIcon)
}

func (app *App) ResolveColor(name string) string {
	return app.iconResolver.ResolveColor(name)
}

func (app *App) ResolveIconColor(name string) string {
	return app.iconResolver.ResolveIconColor(name)
}

func (app *App) ResolveIconCDN(name, explicitIcon string) string {
	return app.iconResolver.ResolveIconCDN(name, explicitIcon)
}
