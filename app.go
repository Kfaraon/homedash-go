package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

// App holds all application dependencies
type App struct {
	// Config
	ConfigFile     string
	CacheTTL       time.Duration
	ServerPort     string
	CheckTimeout   time.Duration
	PingTimeout    time.Duration
	AllowedOrigins string
	// IP settings
	IPProviders []string
	IPCacheTTL  time.Duration
	IPCache     *IPCache
	IPCacheMu   sync.RWMutex

	// Performance settings
	MaxWorkers   int
	LazyInterval time.Duration

	// State
	State *AppState

	// Templates
	HomeTmpl  *template.Template
	AdminTmpl *template.Template

	// Circuit breaker
	circuitBreaker *CircuitBreakerManager

	// Watcher
	Watcher *fsnotify.Watcher
	Done    chan struct{}

	// Mutexes
	reloadMu sync.Mutex // serializes config reloads
	adminMu  sync.Mutex // serializes admin writes (read-modify-save-swap)

	// Lazy evaluation
	lastAccess   atomic.Int64
	isRefreshing atomic.Bool
	idleTimeout  time.Duration

	iconResolver *IconResolver
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

// NewApp creates a fully initialized App
func NewApp() (*App, error) {
	port := getEnv("PORT", "5000")
	if err := validatePort(port); err != nil {
		return nil, err
	}

	checkTimeout, err := getPositiveDurationEnv("CHECK_TIMEOUT", 2*time.Second)
	if err != nil {
		return nil, err
	}
	pingTimeout, err := getPositiveDurationEnv("PING_TIMEOUT", 1*time.Second)
	if err != nil {
		return nil, err
	}
	idleTimeout, err := getPositiveDurationEnv("IDLE_TIMEOUT", 5*time.Minute)
	if err != nil {
		return nil, err
	}
	lazyInterval, err := getPositiveDurationEnv("LAZY_CHECK_INTERVAL", 30*time.Second)
	if err != nil {
		return nil, err
	}
	ipCacheTTL, err := getPositiveDurationEnv("IP_CACHE_TTL", 10*time.Minute)
	if err != nil {
		return nil, err
	}
	maxWorkers, err := getPositiveIntEnv("MAX_WORKERS", 20)
	if err != nil {
		return nil, err
	}

	app := &App{
		ConfigFile:     getEnv("CONFIG_FILE", "config.json"),
		CacheTTL:       15 * time.Second,
		ServerPort:     port,
		CheckTimeout:   checkTimeout,
		PingTimeout:    pingTimeout,
		AllowedOrigins: getEnv("ALLOWED_ORIGINS", ""),
		MaxWorkers:     maxWorkers,
		LazyInterval:   lazyInterval,
		State: &AppState{
			cache: make(map[string]Status),
			stale: make(map[string]Status),
		},
		circuitBreaker: NewCircuitBreakerManager(),
		idleTimeout:    idleTimeout,
		Done:           make(chan struct{}),
	}
	app.lastAccess.Store(time.Now().Unix())
	app.isRefreshing.Store(false)

	app.iconResolver = NewIconResolver()

	// IP providers config. По умолчанию используются сервисы вне популярных
	// VPN-маршрутов (AWS/Google/Hetzner), а не Cloudflare: если часть трафика
	// уходит через VPN по address-list, Cloudflare-провайдеры покажут адрес
	// VPN, а не реальный WAN провайдера.
	providersEnv := getEnv("IP_PROVIDERS", "https://checkip.amazonaws.com,https://ifconfig.me/ip,https://ipinfo.io/ip,https://wtfismyip.com/text")
	app.IPProviders = make([]string, 0, 4)
	for _, p := range strings.Split(providersEnv, ",") {
		if p = strings.TrimSpace(p); p != "" {
			app.IPProviders = append(app.IPProviders, p)
		}
	}
	app.IPCacheTTL = ipCacheTTL
	app.IPCache = &IPCache{}

	// Load and validate config
	cfg, err := loadConfig(app.ConfigFile)
	if err != nil {
		return nil, err
	}
	if err := app.ValidateGroups(cfg.groups); err != nil {
		return nil, fmt.Errorf("некорректная конфигурация %s: %w", app.ConfigFile, err)
	}
	app.SetGroups(cfg.groups)

	// Initialize HTTP transports
	initHTTPTransports()

	// Initialize templates
	if err := app.initTemplates(); err != nil {
		return nil, err
	}

	return app, nil
}

// isActive returns true if there was a request within idleTimeout
func (a *App) isActive() bool {
	return time.Since(time.Unix(a.lastAccess.Load(), 0)) < a.idleTimeout
}

// markAccess updates last access time
func (a *App) markAccess() {
	a.lastAccess.Store(time.Now().Unix())
}

// initTemplates initializes HTML templates
func (app *App) initTemplates() error {
	homeFuncs := template.FuncMap{
		"resolveIcon":    app.ResolveIcon,
		"resolveColor":   app.ResolveColor,
		"resolveIconCDN": app.ResolveIconCDN,
		// iconCandidates возвращает основной URL иконки и запасные из iconify
		// (если сервис не найден в icons.json).
		"iconCandidates": func(name, explicit string) []string {
			return app.iconResolver.IconCandidates(name, explicit)
		},
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
	return cloneGroups(app.State.groups)
}

// GetGroupsCopy returns a safe copy of groups for rendering
func (app *App) GetGroupsCopy() []Group {
	return app.GetGroups()
}

// SetGroups atomically updates groups and clears all caches.
func (app *App) SetGroups(groups []Group) {
	app.State.mu.Lock()
	defer app.State.mu.Unlock()
	app.State.groups = groups
	app.State.cache = make(map[string]Status)
	app.State.cacheTS = time.Time{}
	app.State.stale = make(map[string]Status)
	app.State.staleTS = time.Time{}
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
		return app.State.cache // read-only
	}
	return nil
}

// GetStaleCache returns stale cache if still valid
func (app *App) GetStaleCache() (map[string]Status, bool) {
	app.State.mu.RLock()
	defer app.State.mu.RUnlock()
	if len(app.State.stale) > 0 && time.Since(app.State.staleTS) < 5*app.CacheTTL {
		return app.State.stale, true // read-only
	}
	return nil, false
}

// mergeStatuses overlays freshly checked statuses over the previous cache.
// Services skipped by the circuit breaker keep their last known status instead
// of disappearing from the API response.
func (app *App) mergeStatuses(checked map[string]Status) map[string]Status {
	app.State.mu.RLock()
	base := app.State.cache
	if len(base) == 0 {
		base = app.State.stale
	}
	merged := make(map[string]Status, len(base)+len(checked))
	for k, v := range base {
		merged[k] = v
	}
	app.State.mu.RUnlock()

	for k, v := range checked {
		merged[k] = v
	}
	return merged
}

// SetCache atomically updates cache with stale copy
func (app *App) SetCache(cache map[string]Status) {
	// 1. Prepare stale copy outside the lock to avoid blocking the UI.
	staleCopy := make(map[string]Status, len(cache))
	for k, v := range cache {
		staleCopy[k] = v
	}
	now := time.Now()

	// 2. Swap references under the lock.
	app.State.mu.Lock()
	app.State.cache = cache
	app.State.cacheTS = now
	app.State.stale = staleCopy
	app.State.staleTS = now
	app.State.mu.Unlock()
}

// Run starts the HTTP server and blocks until shutdown
func (app *App) Run() error {
	go app.startLazyCheckLoop()
	app.StartConfigWatcher()
	mux := app.buildRouter()

	srv := &http.Server{
		Addr:         ":" + app.ServerPort,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

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

	close(app.Done)
	closeHTTPTransports()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("Error during shutdown", "error", err)
		return err
	}
	slog.Info("Server stopped successfully")
	return nil
}

// startLazyCheckLoop — цикл с авто-паузой в простое
func (app *App) startLazyCheckLoop() {
	// 1. Мгновенный первичный опрос в фоне
	go app.refreshCacheIfNeeded()

	ticker := time.NewTicker(app.LazyInterval)
	defer ticker.Stop()

	for {
		select {
		case <-app.Done:
			return
		case <-ticker.C:
			if !app.isActive() {
				continue
			}
			app.refreshCacheIfNeeded()
		}
	}
}

// refreshCacheIfNeeded refreshes cache in background
func (app *App) refreshCacheIfNeeded() {
	if !app.isRefreshing.CompareAndSwap(false, true) {
		return
	}
	defer app.isRefreshing.Store(false)

	select {
	case <-app.Done:
		return
	default:
	}

	timeout := app.CheckTimeout + app.PingTimeout + 5*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	done := make(chan struct{})
	go func() {
		select {
		case <-app.Done:
			cancel()
		case <-done:
		}
	}()

	groups := app.GetGroupsCopy()

	// Determine whether this is the initial poll (empty cache).
	isInitialPoll := app.GetCacheCount() == 0

	sm := checkServicesInParallel(ctx, groups, app.circuitBreaker, app.CheckTimeout, app.PingTimeout, app.MaxWorkers, isInitialPoll)

	close(done)

	select {
	case <-app.Done:
		return
	default:
		app.SetCache(app.mergeStatuses(sm))
	}
}

// trackAccessMiddleware updates lastAccess on main endpoints
func (app *App) trackAccessMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/api/status" || r.URL.Path == "/health" {
			app.markAccess()
		}
		next.ServeHTTP(w, r)
	})
}

// buildRouter constructs the HTTP router
func (app *App) buildRouter() http.Handler {
	mux := http.NewServeMux()
	staticHandler := http.StripPrefix("/static/", http.FileServer(http.Dir("static")))
	mux.Handle("/static/", staticHandler)

	mux.Handle("/{$}", app.trackAccessMiddleware(http.HandlerFunc(app.ServeHome)))
	mux.Handle("/api/status", app.trackAccessMiddleware(http.HandlerFunc(app.ServeStatus)))
	mux.Handle("/health", app.trackAccessMiddleware(http.HandlerFunc(app.ServeHealth)))
	mux.HandleFunc("/api/myip", app.ServeMyIP)

	adminMux := http.NewServeMux()
	adminMux.HandleFunc("/admin", app.ServeAdmin)
	adminMux.HandleFunc("/api/admin/groups", app.apiAdminGroups)
	adminMux.HandleFunc("/api/admin/group", app.handleGroupCRUD)
	adminMux.HandleFunc("/api/admin/service/move", app.apiAdminMoveService)
	adminMux.HandleFunc("/api/admin/service/reorder", app.apiAdminReorderServices)
	adminMux.HandleFunc("/api/admin/service", app.handleServiceCRUD)

	adminHandler := app.adminOriginMiddleware(app.rateLimitMiddleware(adminMux))
	mux.Handle("/admin", adminHandler)
	mux.Handle("/admin/{$}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusMovedPermanently)
	}))
	mux.Handle("/api/admin/", adminHandler)

	var handler http.Handler = mux
	handler = app.maxBytesMiddleware(handler)
	handler = app.contentTypeMiddleware(handler)
	handler = app.corsMiddleware(handler)
	handler = app.securityHeadersMiddleware(handler)

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

// jsonMarshalBytes helper
func jsonMarshalBytes(s string) ([]byte, error) {
	return json.Marshal(s)
}

// StartConfigWatcher watches the directory containing the config file, so
// nested paths (e.g. data/config.json) are handled correctly. Watching the
// directory also survives atomic rename-based saves.
func (app *App) StartConfigWatcher() {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Error("Error creating watcher", "error", err)
		return
	}

	configAbs, err := filepath.Abs(app.ConfigFile)
	if err != nil {
		slog.Error("Error resolving config path", "file", app.ConfigFile, "error", err)
		w.Close()
		return
	}
	configAbs = filepath.Clean(configAbs)
	configDir := filepath.Dir(configAbs)

	if err := w.Add(configDir); err != nil {
		slog.Error("Error adding to watcher", "dir", configDir, "error", err)
		w.Close()
		return
	}
	slog.Info("Watching config file", "file", configAbs)

	app.Watcher = w
	triggerCh := make(chan struct{}, 1)

	select {
	case <-app.Done:
		w.Close()
		return
	default:
	}

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
				if !debounceTimer.Stop() {
					select {
					case <-debounceTimer.C:
					default:
					}
				}
				debounceTimer.Reset(500 * time.Millisecond)
			case <-debounceTimer.C:
				slog.Info("Config change detected, reloading...")
				app.reloadConfig()
				select {
				case <-triggerCh:
				default:
				}
			}
		}
	}()

	go func() {
		for {
			select {
			case <-app.Done:
				return
			case e, ok := <-w.Events:
				if !ok {
					return
				}
				eventAbs, err := filepath.Abs(e.Name)
				if err != nil || filepath.Clean(eventAbs) != configAbs {
					continue
				}
				if e.Op&fsnotify.Write != 0 || e.Op&fsnotify.Create != 0 || e.Op&fsnotify.Rename != 0 {
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

// reloadConfig reloads and validates the config file. Invalid configs are
// ignored and the current in-memory groups remain active.
func (app *App) reloadConfig() {
	app.reloadMu.Lock()
	defer app.reloadMu.Unlock()
	cfg, err := loadConfig(app.ConfigFile)
	if err != nil {
		slog.Error("Error reloading config, keeping current config", "error", err)
		return
	}
	g := cfg.groups

	if err := app.ValidateGroups(g); err != nil {
		slog.Error("Config validation failed, keeping current config", "error", err)
		return
	}

	app.iconResolver.ClearCache()
	app.circuitBreaker.Reset()
	app.SetGroups(g)

	n := 0
	for _, gr := range g {
		n += len(gr.Services)
	}
	slog.Info("Config reloaded", "groups", len(g), "services", n)
}

// rateLimitMiddleware applies rate limiting to admin endpoints
func (app *App) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

// rateLimiter implements a simple token bucket rate limiter
type rateLimiter struct {
	mu         sync.Mutex
	tokens     float64
	maxTokens  float64
	refillRate float64
	lastRefill time.Time
}

func newRateLimiter(maxTokens, refillRate float64) *rateLimiter {
	return &rateLimiter{
		tokens:     maxTokens,
		maxTokens:  maxTokens,
		refillRate: refillRate,
		lastRefill: time.Now(),
	}
}

func (rl *rateLimiter) Allow() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(rl.lastRefill).Seconds()
	rl.tokens += rl.refillRate * elapsed
	if rl.tokens > rl.maxTokens {
		rl.tokens = rl.maxTokens
	}
	rl.lastRefill = now

	if rl.tokens < 1 {
		return false
	}
	rl.tokens--
	return true
}

var adminRateLimiter = newRateLimiter(20, 10)
