package main

import (
	"context"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ─── App ───

// App holds all application dependencies (replaces global variables)
type App struct {
	// Config
	ConfigFile       string
	CacheTTL         time.Duration
	ServerPort       string
	CheckTimeout     time.Duration
	PingTimeout      time.Duration
	AdminAPIKey      string
	RequireAdminAuth atomic.Bool // thread-safe, no data race
	AllowedOrigins   string

	// State
	State *AppState

	// Templates
	HomeTmpl  *template.Template
	AdminTmpl *template.Template

	// Metrics
	Metrics *Metrics

	// Watcher
	Watcher *fsnotify.Watcher
	Done    chan struct{} // signal to stop background goroutines

	// Reload mutex to prevent concurrent reloads
	reloadMu sync.Mutex

	// IP settings
	IPProviders    []string      // fallback list
	IPCacheTTL     time.Duration // cache duration
	IPCache        *IPCache
	IPCacheMu      sync.RWMutex
}

// AppState holds runtime state with thread-safe access
type AppState struct {
	mu      sync.RWMutex
	groups  []Group
	cache   map[string]Status
	cacheTS time.Time
	stale   map[string]Status
	staleTS time.Time
}

// NewApp creates a fully initialized App with all dependencies
func NewApp() (*App, error) {
	adminAPIKey := getEnv("ADMIN_API_KEY", "")

	app := &App{
		ConfigFile:     getEnv("CONFIG_FILE", "config.json"),
		CacheTTL:       3 * time.Second,
		ServerPort:     getEnv("PORT", "5000"),
		CheckTimeout:   getDurationEnv("CHECK_TIMEOUT", 2*time.Second),
		PingTimeout:    getDurationEnv("PING_TIMEOUT", 1*time.Second),
		AdminAPIKey:    adminAPIKey,
		AllowedOrigins: getEnv("ALLOWED_ORIGINS", ""),
		State: &AppState{
			cache: make(map[string]Status),
			stale: make(map[string]Status),
		},
		Metrics: NewMetrics(),
		Done:    make(chan struct{}),
	}
	app.RequireAdminAuth.Store(adminAPIKey != "")

	// IP providers config
	providersEnv := getEnv("IP_PROVIDERS", "https://api.ipify.org,https://icanhazip.com,https://ifconfig.co/ip")
	app.IPProviders = strings.Split(providersEnv, ",")
	for i := range app.IPProviders {
    	app.IPProviders[i] = strings.TrimSpace(app.IPProviders[i])
	}
	app.IPCacheTTL = getDurationEnv("IP_CACHE_TTL", 10*time.Minute)
	app.IPCache = &IPCache{}

	// Load and validate config (single file read)
	cfg, err := loadConfig(app.ConfigFile)
	if err != nil {
		return nil, err
	}
	g := cfg.groups
	app.SetGroups(g)

	if err := app.ValidateGroups(g); err != nil {
		slog.Warn("Validation warning", "error", err)
	}

	// Set admin auth from config
	if cfg.admin != nil {
		app.RequireAdminAuth.Store(adminAPIKey != "" && cfg.admin.RequireAPIKey)
	}

	// Initialize HTTP transports
	initHTTPTransports()

	// Initialize templates
	if err := app.initTemplates(); err != nil {
		return nil, err
	}

	return app, nil
}

// ─── Template initialization ───

func (app *App) initTemplates() error {
	homeFuncs := template.FuncMap{
		"resolveIcon":      resolveIcon,
		"resolveColor":     resolveColor,
		"resolveIconColor": resolveIconColor,
		"resolveIconCDN":   resolveIconCDN,
	}

	homeTmpl, err := template.New("home.html").Funcs(homeFuncs).ParseFiles("templates/home.html")
	if err != nil {
		return err
	}
	app.HomeTmpl = homeTmpl

	adminFuncs := template.FuncMap{
		"js": func(s string) template.JS {
			b, _ := jsonMarshalBytes(s)
			return template.JS(b)
		},
	}

	adminTmpl, err := template.New("admin.html").Funcs(adminFuncs).ParseFiles("templates/admin.html")
	if err != nil {
		return err
	}
	app.AdminTmpl = adminTmpl

	return nil
}

// ─── State accessors ───

// LoadGroups loads groups from the config file
func (app *App) LoadGroups() ([]Group, error) {
	return loadGroups(app.ConfigFile)
}

// ValidateGroups validates the group structure
func (app *App) ValidateGroups(groups []Group) error {
	return validateGroups(groups)
}

// SaveGroups saves groups to the config file
func (app *App) SaveGroups(groups []Group) error {
	return saveGroupsToFile(app.ConfigFile, groups)
}

// GetGroups returns a copy of current groups (thread-safe)
func (app *App) GetGroups() []Group {
	app.State.mu.RLock()
	defer app.State.mu.RUnlock()
	result := make([]Group, len(app.State.groups))
	for i, g := range app.State.groups {
		result[i] = Group{
			Name:     g.Name,
			Services: make([]Service, len(g.Services)),
		}
		copy(result[i].Services, g.Services)
	}
	return result
}

// GetGroupsCopy returns a safe copy of groups for rendering
func (app *App) GetGroupsCopy() []Group {
	return app.GetGroups()
}

// SetGroups atomically updates groups and clears cache
func (app *App) SetGroups(groups []Group) {
	app.State.mu.Lock()
	defer app.State.mu.Unlock()
	app.State.groups = groups
	app.State.cache = make(map[string]Status)
	app.State.cacheTS = time.Time{}
}

// SetGroupsNoCacheClear atomically updates groups without clearing cache
func (app *App) SetGroupsNoCacheClear(groups []Group) {
	app.State.mu.Lock()
	defer app.State.mu.Unlock()
	app.State.groups = groups
}

// GetGroupsCount returns number of groups (thread-safe)
func (app *App) GetGroupsCount() int {
	app.State.mu.RLock()
	defer app.State.mu.RUnlock()
	return len(app.State.groups)
}

// GetCacheCount returns number of cached entries (thread-safe)
func (app *App) GetCacheCount() int {
	app.State.mu.RLock()
	defer app.State.mu.RUnlock()
	return len(app.State.cache)
}

// GetCache returns cached statuses or nil if expired
func (app *App) GetCache() map[string]Status {
	app.State.mu.RLock()
	defer app.State.mu.RUnlock()
	if time.Since(app.State.cacheTS) < app.CacheTTL && len(app.State.cache) > 0 {
		result := make(map[string]Status, len(app.State.cache))
		for k, v := range app.State.cache {
			result[k] = v
		}
		return result
	}
	return nil
}

// SetCache atomically updates cache with stale copy
func (app *App) SetCache(cache map[string]Status) {
	app.State.mu.Lock()
	defer app.State.mu.Unlock()
	app.State.cache = cache
	app.State.cacheTS = time.Now()
	// Save stale copy
	app.State.stale = make(map[string]Status, len(cache))
	for k, v := range cache {
		app.State.stale[k] = v
	}
	app.State.staleTS = time.Now()
}

// GetStaleCache returns stale cache if still valid
func (app *App) GetStaleCache() (map[string]Status, bool) {
	app.State.mu.RLock()
	defer app.State.mu.RUnlock()
	if len(app.State.stale) > 0 && time.Since(app.State.staleTS) < app.CacheTTL*5 {
		result := make(map[string]Status, len(app.State.stale))
		for k, v := range app.State.stale {
			result[k] = v
		}
		return result, true
	}
	return nil, false
}

// ─── Server lifecycle ───

// Run starts the HTTP server and blocks until shutdown
func (app *App) Run() error {
	// Start config watcher
	app.StartConfigWatcher()

	// Build router
	mux := app.buildRouter()

	// Server with timeouts
	srv := &http.Server{
		Addr:         ":" + app.ServerPort,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	// Graceful shutdown
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		slog.Info("Server started", "url", "http://localhost:"+app.ServerPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("Error starting server", "error", err)
			os.Exit(1)
		}
	}()

	<-done
	slog.Info("Shutdown signal received, shutting down...")

	// Signal background goroutines to stop
	close(app.Done)

	// Close idle HTTP connections
	getHTTPTransport().CloseIdleConnections()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("Error during shutdown", "error", err)
		return err
	}
	slog.Info("Server stopped successfully")
	return nil
}

// ─── Router ───

func (app *App) buildRouter() http.Handler {
	mux := http.NewServeMux()

	// Static files
	staticHandler := http.StripPrefix("/static/", http.FileServer(http.Dir("static")))
	mux.Handle("/static/", staticHandler)

	// Main routes
	mux.HandleFunc("/", app.ServeHome)
	mux.HandleFunc("/api/status", app.ServeStatus)
	mux.HandleFunc("/health", app.ServeHealth)
	mux.HandleFunc("/api/myip", app.ServeMyIP)
	mux.HandleFunc("/api/metrics", app.ServeMetrics)
	mux.HandleFunc("/metrics", app.ServePrometheusMetrics)

	// Admin routes (auth + rate limiting applied BEFORE outer middleware)
	adminMux := http.NewServeMux()
	adminMux.HandleFunc("/admin", app.ServeAdmin)
	adminMux.HandleFunc("/api/admin/groups", app.apiAdminGroups)
	adminMux.HandleFunc("/api/admin/group", app.handleGroupCRUD)
	adminMux.HandleFunc("/api/admin/service/move", app.apiAdminMoveService)
	adminMux.HandleFunc("/api/admin/service/reorder", app.apiAdminReorderServices)
	adminMux.HandleFunc("/api/admin/service", app.handleServiceCRUD)

	// Wrap admin routes with auth + rate limiting
	adminHandler := app.rateLimitMiddleware(app.adminAuthMiddleware(adminMux))
	mux.Handle("/admin", adminHandler)
	mux.Handle("/api/admin/", adminHandler)

	// Middleware chain: apply in reverse order of execution
	// Public routes order: CORS → ContentType → MaxBytes → Handler
	// Admin routes order: CORS → Auth → RateLimit → ContentType → MaxBytes → Handler
	var handler http.Handler = mux
	handler = app.maxBytesMiddleware(handler)
	handler = app.contentTypeMiddleware(handler)
	handler = app.corsMiddleware(handler)

	return handler
}

func (app *App) handleGroupCRUD(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		app.apiAdminGroups(w, r)
	case http.MethodPost:
		app.apiAdminAddGroup(w, r)
	case http.MethodDelete:
		app.apiAdminDeleteGroup(w, r)
	case http.MethodPut:
		app.apiAdminRenameGroup(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (app *App) handleServiceCRUD(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		app.apiAdminAddService(w, r)
	case http.MethodPut:
		app.apiAdminUpdateService(w, r)
	case http.MethodDelete:
		app.apiAdminDeleteService(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// ─── Helpers ───

func jsonMarshalBytes(s string) ([]byte, error) {
	return json.Marshal(s)
}

// ─── Config Watcher ───

// StartConfigWatcher starts watching the config file for changes
func (app *App) StartConfigWatcher() {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Error("Error creating watcher", "error", err)
		return
	}

	if err := w.Add("."); err != nil {
		slog.Error("Error adding to watcher", "file", app.ConfigFile, "error", err)
		w.Close()
		return
	}
	slog.Info("Watching config file", "file", app.ConfigFile)

	app.Watcher = w
	triggerCh := make(chan struct{}, 1)

	// Check if app is already shutting down
	select {
	case <-app.Done:
		w.Close()
		return
	default:
	}

	// Debounce goroutine
	go func() {
		defer w.Close()
		debounceTimer := time.NewTimer(500 * time.Millisecond)
		debounceTimer.Stop()

		for {
			select {
			case <-app.Done:
				return
			case _, ok := <-triggerCh:
				if !ok {
					return
				}
				// Stop existing timer before resetting to prevent double fire
				if !debounceTimer.Stop() {
					<-debounceTimer.C
				}
				debounceTimer.Reset(500 * time.Millisecond)
			case <-debounceTimer.C:
				slog.Info("Config.json change detected, reloading...")
				app.reloadConfig()
				// Drain any pending events that arrived during reload
				select {
				case <-triggerCh:
				default:
				}
			}
		}
	}()

	// Event loop (runs until app.Done is closed or watcher errors out)
	go func() {
		for {
			select {
			case <-app.Done:
				return
			case e, ok := <-w.Events:
				if !ok {
					return
				}
				if e.Name == app.ConfigFile+".tmp" {
					continue
				}
				if e.Name == app.ConfigFile && (e.Op&fsnotify.Write != 0 || e.Op&fsnotify.Create != 0 || e.Op&fsnotify.Rename != 0) {
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
	}()
}

// reloadConfig reloads and validates the config file
func (app *App) reloadConfig() {
	// Prevent concurrent reloads
	app.reloadMu.Lock()
	defer app.reloadMu.Unlock()

	cfg, err := loadConfig(app.ConfigFile)
	if err != nil {
		slog.Error("Error reloading config", "error", err)
		return
	}
	g := cfg.groups

	if err := app.ValidateGroups(g); err != nil {
		slog.Warn("Validation warning", "error", err)
	}

	// Update admin config
	if cfg.admin != nil {
		app.RequireAdminAuth.Store(app.AdminAPIKey != "" && cfg.admin.RequireAPIKey)
	}

	app.SetGroups(g)
	app.Metrics.Reset()

	n := 0
	for _, gr := range g {
		n += len(gr.Services)
	}

	app.Metrics.IncrementConfigReloads()
	slog.Info("Config reloaded", "groups", len(g), "services", n)
}

// ─── Rate Limiter ───

// rateLimiter implements a simple token bucket rate limiter
type rateLimiter struct {
	mu         sync.Mutex
	tokens     int64
	maxTokens  int64
	refillRate int64 // tokens per second
	lastRefill time.Time
}

// newRateLimiter creates a rate limiter
func newRateLimiter(maxTokens, refillRate int64) *rateLimiter {
	return &rateLimiter{
		tokens:     maxTokens,
		maxTokens:  maxTokens,
		refillRate: refillRate,
		lastRefill: time.Now(),
	}
}

// Allow returns true if the request is allowed
func (rl *rateLimiter) Allow() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(rl.lastRefill).Seconds()
	rl.tokens += int64(float64(rl.refillRate) * elapsed)
	if rl.tokens > rl.maxTokens {
		rl.tokens = rl.maxTokens
	}
	rl.lastRefill = now

	if rl.tokens <= 0 {
		return false
	}
	rl.tokens--
	return true
}

// Global rate limiter for admin endpoints (10 requests/second, burst 20)
//
//nolint:gochecknoglobals
var adminRateLimiter = newRateLimiter(20, 10)

// rateLimitMiddleware applies rate limiting to admin endpoints
func (app *App) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip rate limiting for CORS preflight
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if !adminRateLimiter.Allow() {
			http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
