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
func (app *App) ServeStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(app.getCachedStatuses()); err != nil {
		slog.Debug("Failed to encode status response", "error", err)
	}
}

// ServeHealth — handler for health check. Returns 503 when the config cannot
// be loaded, so container orchestrators can detect a broken deployment.
func (app *App) ServeHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	configOK := true
	if _, err := app.LoadGroups(); err != nil {
		slog.Warn("Health check: config load failed", "error", err)
		configOK = false
	}

	status := "ok"
	statusCode := http.StatusOK
	if !configOK {
		status = "degraded"
		statusCode = http.StatusServiceUnavailable
	}

	w.WriteHeader(statusCode)
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
		http.Error(w, "Не удалось определить публичный IP-адрес", http.StatusBadGateway)
		return
	}

	app.updateIPCache(ip, ipType, provider)

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
		parsed := net.ParseIP(ip)
		if parsed == nil || !isPublicIP(parsed) {
			slog.Debug("IP provider returned a non-public address, skipping",
				"provider", providerURL, "value", ip)
			ip = ""
			continue
		}

		if parsed.To4() != nil {
			ipType = "ipv4"
		} else {
			ipType = "ipv6"
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

// isPublicIP reports whether the address is a globally routable unicast address.
// It rejects loopback, link-local, private (RFC1918), CGNAT (100.64/10),
// IPv6 ULA (fc00::/7) and IPv6 link-local/loopback addresses, so a broken or
// misbehaving provider cannot make the UI show a LAN address as the WAN IP.
func isPublicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() ||
		ip.IsPrivate() { // RFC1918 + IPv6 ULA (fc00::/7)
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		// CGNAT 100.64.0.0/10 — not routable on the public Internet.
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return false
		}
		// Benchmark 198.18.0.0/15 and documentation/test ranges.
		if v4[0] == 198 && (v4[1] == 18 || v4[1] == 19 ||
			(v4[1] == 51 && v4[2] == 100)) {
			return false
		}
		if v4[0] == 192 && v4[1] == 0 && (v4[2] == 0 || v4[2] == 2) {
			return false
		}
		if v4[0] == 203 && v4[1] == 0 && v4[2] == 113 {
			return false
		}
	}
	return true
}

// isAdminPath reports whether the path belongs to the admin UI/API.
func isAdminPath(path string) bool {
	return path == "/admin" || strings.HasPrefix(path, "/admin/") ||
		path == "/api/admin" || strings.HasPrefix(path, "/api/admin/")
}

// sameOrigin reports whether the Origin header matches the request host.
func sameOrigin(origin, host string) bool {
	if origin == "" || origin == "null" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, host)
}

// corsMiddleware — CORS for public endpoints and strict same-origin handling
// for the admin UI/API (which is intentionally available without authentication).
func (app *App) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		if isAdminPath(r.URL.Path) {
			if origin != "" {
				if !sameOrigin(origin, r.Host) {
					http.Error(w, "Cross-origin admin requests are not allowed", http.StatusForbidden)
					return
				}
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

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

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// adminOriginMiddleware rejects cross-origin admin requests as defence in depth.
func (app *App) adminOriginMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r.Host) {
			http.Error(w, "Cross-origin admin requests are not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeadersMiddleware adds basic hardening headers.
func (app *App) securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if isAdminPath(r.URL.Path) {
			w.Header().Set("X-Frame-Options", "DENY")
		}
		next.ServeHTTP(w, r)
	})
}

// maxBytesMiddleware — request body size limit (1MB)
func (app *App) maxBytesMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
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

// getCachedStatuses returns cached statuses or stale/empty fallback
func (app *App) getCachedStatuses() map[string]any {
	if cache := app.GetCache(); cache != nil {
		return app.statusResp(cache)
	}

	if stale, ok := app.GetStaleCache(); ok {
		go app.refreshCacheIfNeeded()
		return app.statusResp(stale)
	}

	go app.refreshCacheIfNeeded()
	return app.statusResp(make(map[string]Status))
}

func (app *App) statusResp(services map[string]Status) map[string]any {
	return map[string]any{
		"services":  services,
		"timestamp": time.Now().Format(time.RFC3339),
	}
}

// Icon helpers
func (app *App) ResolveIcon(name, explicitIcon string) string {
	return app.iconResolver.ResolveIcon(name, explicitIcon)
}

func (app *App) ResolveColor(name string) string {
	return app.iconResolver.ResolveColor(name)
}

func (app *App) ResolveIconCDN(name, explicitIcon string) string {
	return app.iconResolver.ResolveIconCDN(name, explicitIcon)
}
