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

// App holds all application dependencies
type App struct {
	// Config
	ConfigFile       string
	CacheTTL         time.Duration
	ServerPort       string
	CheckTimeout     time.Duration
	PingTimeout      time.Duration
	AdminAPIKey      string
	RequireAdminAuth atomic.Bool
	AllowedOrigins   string
	// IP settings
	IPProviders []string
	IPCacheTTL  time.Duration
	IPCache     *IPCache
	IPCacheMu   sync.RWMutex

	// Performance settings
	MaxWorkers       int
	UseLRUCache      bool
	LRUCacheCapacity int
	LRUCacheTTL      time.Duration

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

	// Reload mutex
	reloadMu sync.Mutex

	// Lazy evaluation
	lastAccess   atomic.Int64
	checkActive  atomic.Bool
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
	adminAPIKey := getEnv("ADMIN_API_KEY", "")
	app := &App{
		ConfigFile:     getEnv("CONFIG_FILE", "config.json"),
		CacheTTL:       3 * time.Second,
		ServerPort:     getEnv("PORT", "5000"),
		CheckTimeout:   getDurationEnv("CHECK_TIMEOUT", 2*time.Second),
		PingTimeout:    getDurationEnv("PING_TIMEOUT", 1*time.Second),
		AdminAPIKey:    adminAPIKey,
		AllowedOrigins: getEnv("ALLOWED_ORIGINS", ""),
		MaxWorkers:     getIntEnv("MAX_WORKERS", 20),
		State: &AppState{
			cache: make(map[string]Status),
			stale: make(map[string]Status),
		},
		circuitBreaker: NewCircuitBreakerManager(),
		idleTimeout:    getDurationEnv("IDLE_TIMEOUT", 5*time.Minute),
		Done:           make(chan struct{}),
	}
	app.lastAccess.Store(time.Now().Unix())
	app.checkActive.Store(false)
	app.isRefreshing.Store(false)
	app.RequireAdminAuth.Store(adminAPIKey != "")

	app.iconResolver = NewIconResolver()

	// IP providers config
	providersEnv := getEnv("IP_PROVIDERS", "https://api.ipify.org,https://icanhazip.com,https://ifconfig.co/ip")
	app.IPProviders = strings.Split(providersEnv, ",")
	for i := range app.IPProviders {
		app.IPProviders[i] = strings.TrimSpace(app.IPProviders[i])
	}
	app.IPCacheTTL = getDurationEnv("IP_CACHE_TTL", 10*time.Minute)
	app.IPCache = &IPCache{}

	// Load and validate config
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
		"resolveIcon":      app.ResolveIcon,
		"resolveColor":     app.ResolveColor,
		"resolveIconColor": app.ResolveIconColor,
		"resolveIconCDN":   app.ResolveIconCDN,
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
	if len(app.State.stale) > 0 && time.Since(app.State.staleTS) < 5*app.CacheTTL {
		result := make(map[string]Status, len(app.State.stale))
		for k, v := range app.State.stale {
			result[k] = v
		}
		return result, true
	}
	return nil, false
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

// startLazyCheckLoop — цикл с авто-паузой в простое
func (app *App) startLazyCheckLoop() {
	interval := getDurationEnv("LAZY_CHECK_INTERVAL", 30*time.Second)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-app.Done:
			app.checkActive.Store(false)
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

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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
	sm := checkServicesInParallel(ctx, groups, app.circuitBreaker, app.PingTimeout, app.MaxWorkers)

	close(done)

	select {
	case <-app.Done:
		return
	default:
		app.SetCache(sm)
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

	mux.Handle("/", app.trackAccessMiddleware(http.HandlerFunc(app.ServeHome)))
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

	adminHandler := app.rateLimitMiddleware(app.adminAuthMiddleware(adminMux))
	mux.Handle("/admin", adminHandler)
	mux.Handle("/api/admin/", adminHandler)

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

// jsonMarshalBytes helper
func jsonMarshalBytes(s string) ([]byte, error) {
	return json.Marshal(s)
}

// StartConfigWatcher watches config file changes
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
					<-debounceTimer.C
				}
				debounceTimer.Reset(500 * time.Millisecond)
			case <-debounceTimer.C:
				slog.Info("Config.json change detected, reloading...")
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

	if cfg.admin != nil {
		app.RequireAdminAuth.Store(app.AdminAPIKey != "" && cfg.admin.RequireAPIKey)
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
	tokens     int64
	maxTokens  int64
	refillRate int64
	lastRefill time.Time
}

func newRateLimiter(maxTokens, refillRate int64) *rateLimiter {
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

var adminRateLimiter = newRateLimiter(20, 10)
