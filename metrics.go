package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Metrics collects counters for monitoring
type Metrics struct {
	mu sync.RWMutex

	// Check counters
	ChecksTotal   map[string]int64
	ChecksFailed  map[string]int64
	CheckDuration map[string]time.Duration

	// Global counters
	ConfigReloads int64
	CacheHits     int64
	CacheMisses   int64
	ActiveChecks  int64

	// Circuit breaker data
	CircuitBreaker map[string]*CircuitState
}

// CircuitState stores circuit breaker state for a service
type CircuitState struct {
	Failures    int
	LastFailure time.Time
	State       CircuitStateEnum // closed, open, half-open
	LastCheck   time.Time
	MinInterval time.Duration
}

// CircuitStateEnum — circuit breaker state
type CircuitStateEnum int

const (
	CircuitClosed CircuitStateEnum = iota
	CircuitOpen
	CircuitHalfOpen
)

// NewMetrics creates a new metrics struct
func NewMetrics() *Metrics {
	return &Metrics{
		ChecksTotal:    make(map[string]int64),
		ChecksFailed:   make(map[string]int64),
		CheckDuration:  make(map[string]time.Duration),
		CircuitBreaker: make(map[string]*CircuitState),
	}
}

// RecordCheck records a service check result
func (m *Metrics) RecordCheck(name string, success bool, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.ChecksTotal[name]++
	if !success {
		m.ChecksFailed[name]++
	}
	m.CheckDuration[name] = duration

	// Update circuit breaker
	cb := m.getCircuitStateLocked(name)
	cb.LastCheck = time.Now()

	if success {
		cb.Failures = 0
		cb.State = CircuitClosed
	} else {
		cb.Failures++
		cb.LastFailure = time.Now()
		if cb.Failures >= 3 {
			cb.State = CircuitOpen
		}
	}
}

// getCircuitStateLocked gets or creates circuit state for a service
func (m *Metrics) getCircuitStateLocked(name string) *CircuitState {
	cb, exists := m.CircuitBreaker[name]
	if !exists {
		cb = &CircuitState{
			State:       CircuitClosed,
			MinInterval: 5 * time.Second,
		}
		m.CircuitBreaker[name] = cb
	}
	return cb
}

// ShouldCheck returns true if the service should be checked
// (circuit breaker + rate limiting)
func (m *Metrics) ShouldCheck(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cb, exists := m.CircuitBreaker[name]
	if !exists {
		return true
	}

	// Rate limiting: minimum interval between checks
	if time.Since(cb.LastCheck) < cb.MinInterval {
		return false
	}

	// Circuit open — skip check
	if cb.State == CircuitOpen {
		// Allow check once every 30 seconds for probing
		if time.Since(cb.LastFailure) > 30*time.Second {
			return true
		}
		return false
	}

	return true
}

// GetCircuitState returns the circuit breaker state
func (m *Metrics) GetCircuitState(name string) CircuitStateEnum {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if cb, exists := m.CircuitBreaker[name]; exists {
		return cb.State
	}
	return CircuitClosed
}

// IncrementConfigReloads increments the config reload counter
func (m *Metrics) IncrementConfigReloads() {
	atomic.AddInt64(&m.ConfigReloads, 1)
}

// IncrementCacheHits increments the cache hits counter
func (m *Metrics) IncrementCacheHits() {
	atomic.AddInt64(&m.CacheHits, 1)
}

// IncrementCacheMisses increments the cache misses counter
func (m *Metrics) IncrementCacheMisses() {
	atomic.AddInt64(&m.CacheMisses, 1)
}

// AddActiveCheck increments the active checks counter
func (m *Metrics) AddActiveCheck(delta int64) {
	atomic.AddInt64(&m.ActiveChecks, delta)
}

// GetSnapshot returns a metrics snapshot for the API
func (m *Metrics) GetSnapshot() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Copy data for thread-safe access
	checksTotal := make(map[string]int64, len(m.ChecksTotal))
	checksFailed := make(map[string]int64, len(m.ChecksFailed))
	checkDuration := make(map[string]float64, len(m.CheckDuration))
	circuits := make(map[string]string, len(m.CircuitBreaker))

	for k, v := range m.ChecksTotal {
		checksTotal[k] = v
	}
	for k, v := range m.ChecksFailed {
		checksFailed[k] = v
	}
	for k, v := range m.CheckDuration {
		checkDuration[k] = v.Seconds()
	}
	for k, v := range m.CircuitBreaker {
		stateStr := "closed"
		switch v.State {
		case CircuitOpen:
			stateStr = "open"
		case CircuitHalfOpen:
			stateStr = "half-open"
		}
		circuits[k] = stateStr
	}

	return map[string]any{
		"checks_total":     checksTotal,
		"checks_failed":    checksFailed,
		"check_duration_s": checkDuration,
		"config_reloads":   atomic.LoadInt64(&m.ConfigReloads),
		"cache_hits":       atomic.LoadInt64(&m.CacheHits),
		"cache_misses":     atomic.LoadInt64(&m.CacheMisses),
		"active_checks":    atomic.LoadInt64(&m.ActiveChecks),
		"circuit_breakers": circuits,
		"timestamp":        time.Now().Format(time.RFC3339),
	}
}

// Reset clears all metrics without recreating the object (thread-safe)
func (m *Metrics) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.ChecksTotal = make(map[string]int64)
	m.ChecksFailed = make(map[string]int64)
	m.CheckDuration = make(map[string]time.Duration)
	m.CircuitBreaker = make(map[string]*CircuitState)
	atomic.StoreInt64(&m.ConfigReloads, 0)
	atomic.StoreInt64(&m.CacheHits, 0)
	atomic.StoreInt64(&m.CacheMisses, 0)
	atomic.StoreInt64(&m.ActiveChecks, 0)
}

// GetPrometheusMetrics returns metrics in Prometheus text format
func (m *Metrics) GetPrometheusMetrics() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var sb strings.Builder

	// Global counters
	sb.WriteString("# HELP homedash_config_reloads_total Total number of config reloads\n")
	sb.WriteString("# TYPE homedash_config_reloads_total counter\n")
	sb.WriteString(fmt.Sprintf("homedash_config_reloads_total %d\n", atomic.LoadInt64(&m.ConfigReloads)))

	sb.WriteString("# HELP homedash_cache_hits_total Total number of cache hits\n")
	sb.WriteString("# TYPE homedash_cache_hits_total counter\n")
	sb.WriteString(fmt.Sprintf("homedash_cache_hits_total %d\n", atomic.LoadInt64(&m.CacheHits)))

	sb.WriteString("# HELP homedash_cache_misses_total Total number of cache misses\n")
	sb.WriteString("# TYPE homedash_cache_misses_total counter\n")
	sb.WriteString(fmt.Sprintf("homedash_cache_misses_total %d\n", atomic.LoadInt64(&m.CacheMisses)))

	sb.WriteString("# HELP homedash_active_checks Number of currently active checks\n")
	sb.WriteString("# TYPE homedash_active_checks gauge\n")
	sb.WriteString(fmt.Sprintf("homedash_active_checks %d\n", atomic.LoadInt64(&m.ActiveChecks)))

	// Per-service metrics
	sb.WriteString("# HELP homedash_checks_total Total number of checks per service\n")
	sb.WriteString("# TYPE homedash_checks_total counter\n")
	for name, count := range m.ChecksTotal {
		sb.WriteString(fmt.Sprintf("homedash_checks_total{service=%q} %d\n", name, count))
	}

	sb.WriteString("# HELP homedash_checks_failed_total Total number of failed checks per service\n")
	sb.WriteString("# TYPE homedash_checks_failed_total counter\n")
	for name, count := range m.ChecksFailed {
		sb.WriteString(fmt.Sprintf("homedash_checks_failed_total{service=%q} %d\n", name, count))
	}

	sb.WriteString("# HELP homedash_check_duration_seconds Last check duration in seconds per service\n")
	sb.WriteString("# TYPE homedash_check_duration_seconds gauge\n")
	for name, dur := range m.CheckDuration {
		sb.WriteString(fmt.Sprintf("homedash_check_duration_seconds{service=%q} %.3f\n", name, dur.Seconds()))
	}

	// Circuit breaker states
	sb.WriteString("# HELP homedash_circuit_breaker_state Circuit breaker state per service (0=closed, 1=open, 2=half-open)\n")
	sb.WriteString("# TYPE homedash_circuit_breaker_state gauge\n")
	for name, cb := range m.CircuitBreaker {
		stateVal := 0
		switch cb.State {
		case CircuitOpen:
			stateVal = 1
		case CircuitHalfOpen:
			stateVal = 2
		}
		sb.WriteString(fmt.Sprintf("homedash_circuit_breaker_state{service=%q} %d\n", name, stateVal))
	}

	return sb.String()
}

// CheckResult stores the result of a single check
type CheckResult struct {
	Name     string
	Status   Status
	Duration time.Duration
}

// checkServicesInParallel checks all services in parallel with a worker pool
func checkServicesInParallel(ctx context.Context, groups []Group, metrics *Metrics) map[string]Status {
	// Collect all services
	type serviceWithGroup struct {
		Group string
		Svc   Service
	}

	var services []serviceWithGroup
	for _, g := range groups {
		for _, s := range g.Services {
			services = append(services, serviceWithGroup{Group: g.Name, Svc: s})
		}
	}

	if len(services) == 0 {
		return make(map[string]Status)
	}

	// Worker pool: limit concurrency (builtin min Go 1.21)
	maxWorkers := len(services)
	if maxWorkers > 20 {
		maxWorkers = 20
	}
	sem := make(chan struct{}, maxWorkers)

	resultCh := make(chan CheckResult, len(services))
	var wg sync.WaitGroup

	for _, sg := range services {
		// Check circuit breaker / rate limiting
		if !metrics.ShouldCheck(sg.Svc.Name) {
			continue
		}

		wg.Add(1)
		go func(svc Service, grp string) {
			defer wg.Done()

			sem <- struct{}{}        // acquire
			defer func() { <-sem }() // release

			metrics.AddActiveCheck(1)
			defer metrics.AddActiveCheck(-1)

			start := time.Now()
			st := checkService(ctx, svc)
			duration := time.Since(start)

			metrics.RecordCheck(svc.Name, st.Available, duration)

			resultCh <- CheckResult{
				Name:     svc.Name,
				Status:   st,
				Duration: duration,
			}
		}(sg.Svc, sg.Group)
	}

	// Close channel after all goroutines finish
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Collect results
	sm := make(map[string]Status)
	for result := range resultCh {
		sm[result.Name] = result.Status
	}

	return sm
}
