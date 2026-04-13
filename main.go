package main

import (
	"context"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ─── Config ───

var (
	configFile     = getEnv("CONFIG_FILE", "config.json")
	cacheTTL       = 3 * time.Second
	serverPort     = getEnv("PORT", "5000")
	checkTimeout   = getDurationEnv("CHECK_TIMEOUT", 2*time.Second)
	pingTimeout    = getDurationEnv("PING_TIMEOUT", 1*time.Second)
	adminAPIKey    = getEnv("ADMIN_API_KEY", "")
	allowedOrigins = getEnv("ALLOWED_ORIGINS", "")

	// Admin template
	adminTmpl *template.Template

	// Global state with mutex
	state = struct {
		groupsMu     sync.RWMutex
		groups       []Group
		cacheMu      sync.RWMutex
		cache        map[string]Status
		cacheTS      time.Time
		staleCache   map[string]Status
		staleCacheTS time.Time
	}{
		cache:      make(map[string]Status),
		staleCache: make(map[string]Status),
	}

	// Metrics
	metrics *Metrics
)

// ─── Env helpers ───

// getEnv retrieves environment variable with fallback
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// getDurationEnv retrieves environment variable as duration
func getDurationEnv(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

// ─── Main ───

func main() {
	// Initialize metrics
	metrics = NewMetrics()

	// Load and validate config
	var err error
	state.groupsMu.Lock()
	state.groups, err = loadGroups(configFile)
	state.groupsMu.Unlock()
	if err != nil {
		slog.Error("Error loading config", "error", err)
		os.Exit(1)
	}
	if err := validateGroups(state.groups); err != nil {
		slog.Warn("Validation warning", "error", err)
	}

	// Config hot-reload
	go watchConfig()

	// Template with functions
	funcs := template.FuncMap{
		"resolveIcon":      resolveIcon,
		"resolveColor":     resolveColor,
		"resolveIconColor": resolveIconColor,
		"resolveIconCDN":   resolveIconCDN,
	}
	tmpl := template.Must(template.New("home.html").Funcs(funcs).ParseFiles("templates/home.html"))

	// Router
	mux := http.NewServeMux()

	// Static files with caching
	staticHandler := http.StripPrefix("/static/", http.FileServer(http.Dir("static")))
	mux.Handle("/static/", staticHandler)

	// Main routes
	mux.HandleFunc("/", serveHome(tmpl))
	mux.HandleFunc("/api/status", serveStatus)
	mux.HandleFunc("/health", serveHealth)
	mux.HandleFunc("/api/metrics", serveMetrics)
	mux.HandleFunc("/metrics", servePrometheusMetrics)

	// Admin panel (protected by auth middleware)
	adminFuncs := template.FuncMap{
		"js": func(s string) template.JS {
			// json.Marshal returns a JS-quoted string with escaping
			// template.JS is NOT escaped in HTML, so it will be inserted as-is in onclick
			b, _ := json.Marshal(s)
			return template.JS(b)
		},
	}
	adminTmpl = template.Must(template.New("admin.html").Funcs(adminFuncs).ParseFiles("templates/admin.html"))

	// Admin routes with auth middleware
	adminMux := http.NewServeMux()
	adminMux.HandleFunc("/admin", serveAdmin)
	adminMux.HandleFunc("/api/admin/groups", apiAdminGroups)
	adminMux.HandleFunc("/api/admin/group", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			apiAdminGroups(w, r)
		case http.MethodPost:
			apiAdminAddGroup(w, r)
		case http.MethodDelete:
			apiAdminDeleteGroup(w, r)
		case http.MethodPut:
			apiAdminRenameGroup(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	adminMux.HandleFunc("/api/admin/service/move", apiAdminMoveService)
	adminMux.HandleFunc("/api/admin/service/reorder", apiAdminReorderServices)
	adminMux.HandleFunc("/api/admin/service", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			apiAdminAddService(w, r)
		case http.MethodPut:
			apiAdminUpdateService(w, r)
		case http.MethodDelete:
			apiAdminDeleteService(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.Handle("/admin", adminAuthMiddleware(adminMux))
	mux.Handle("/api/admin/", adminAuthMiddleware(adminMux))

	// Middleware chain
	var handler http.Handler = mux
	handler = maxBytesMiddleware(handler)
	handler = contentTypeMiddleware(handler)
	handler = corsMiddleware(handler)

	// Server with timeouts
	srv := &http.Server{
		Addr:         ":" + serverPort,
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	// Graceful shutdown
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		slog.Info("Server started", "url", "http://localhost:"+serverPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("Error starting server", "error", err)
			os.Exit(1)
		}
	}()

	<-done
	slog.Info("Shutdown signal received, shutting down...")

	// Close idle HTTP transport connections
	if transportSecure != nil {
		transportSecure.CloseIdleConnections()
	}
	if transportInsecure != nil {
		transportInsecure.CloseIdleConnections()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("Error during shutdown", "error", err)
		os.Exit(1)
	}
	slog.Info("Server stopped successfully")
}

// ─── Config loading & watch ───

func watchConfig() {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Error("Error creating watcher", "error", err)
		return
	}
	defer w.Close()

	if err := w.Add("."); err != nil {
		slog.Error("Error adding to watcher", "file", configFile, "error", err)
		return
	}
	slog.Info("Watching config file", "file", configFile)

	// Debounce: trigger channel + separate goroutine
	triggerCh := make(chan struct{}, 1)

	go func() {
		debounceTimer := time.NewTimer(500 * time.Millisecond)
		debounceTimer.Stop()

		for {
			select {
			case _, ok := <-triggerCh:
				if !ok {
					return
				}
				// Reset timer on each event
				debounceTimer.Reset(500 * time.Millisecond)
			case <-debounceTimer.C:
				slog.Info("Config.json change detected, reloading...")
				reloadConfig()
			}
		}
	}()

	for {
		select {
		case e, ok := <-w.Events:
			if !ok {
				return
			}
			// Ignore .tmp files (atomic write)
			if e.Name == configFile+".tmp" {
				continue
			}
			if e.Name == configFile && (e.Op&fsnotify.Write != 0 || e.Op&fsnotify.Create != 0 || e.Op&fsnotify.Rename != 0) {
				// Non-blocking trigger send
				select {
				case triggerCh <- struct{}{}:
				default:
				}
			}
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			slog.Error("Watcher error", "error", err)
		}
	}
}

func reloadConfig() {
	g, err := loadGroups(configFile)
	if err != nil {
		slog.Error("Error reloading config", "error", err)
		return
	}
	if err := validateGroups(g); err != nil {
		slog.Warn("Validation warning", "error", err)
	}

	// Atomic update: groups + cache under single lock
	state.groupsMu.Lock()
	state.groups = g
	state.cacheMu.Lock()
	state.cache = make(map[string]Status)
	state.cacheTS = time.Time{}
	state.cacheMu.Unlock()
	state.groupsMu.Unlock()

	// Thread-safe metrics reset (no data race)
	metrics.Reset()

	n := 0
	for _, gr := range g {
		n += len(gr.Services)
	}

	metrics.IncrementConfigReloads()
	slog.Info("Config reloaded", "groups", len(g), "services", n)
}
