package main

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// ─── NewMetrics tests ───

func TestNewMetrics_Initialization(t *testing.T) {
	m := NewMetrics()
	if m.ChecksTotal == nil {
		t.Error("ChecksTotal not initialized")
	}
	if m.ChecksFailed == nil {
		t.Error("ChecksFailed not initialized")
	}
	if m.CheckDuration == nil {
		t.Error("CheckDuration not initialized")
	}
	if m.CircuitBreaker == nil {
		t.Error("CircuitBreaker not initialized")
	}
}

// ─── RecordCheck tests ───

func TestRecordCheck_Success(t *testing.T) {
	m := NewMetrics()
	m.RecordCheck("web", true, 100*time.Millisecond)

	if m.ChecksTotal["web"] != 1 {
		t.Errorf("expected ChecksTotal=1, got %d", m.ChecksTotal["web"])
	}
	if m.ChecksFailed["web"] != 0 {
		t.Errorf("expected ChecksFailed=0, got %d", m.ChecksFailed["web"])
	}
	if m.CheckDuration["web"] != 100*time.Millisecond {
		t.Errorf("expected CheckDuration=100ms, got %v", m.CheckDuration["web"])
	}
}

func TestRecordCheck_Failure(t *testing.T) {
	m := NewMetrics()
	m.RecordCheck("web", false, 200*time.Millisecond)

	if m.ChecksTotal["web"] != 1 {
		t.Errorf("expected ChecksTotal=1, got %d", m.ChecksTotal["web"])
	}
	if m.ChecksFailed["web"] != 1 {
		t.Errorf("expected ChecksFailed=1, got %d", m.ChecksFailed["web"])
	}
}

func TestRecordCheck_CircuitBreakerOpens(t *testing.T) {
	m := NewMetrics()
	// 3 consecutive failures should open circuit
	m.RecordCheck("web", false, 0)
	m.RecordCheck("web", false, 0)
	m.RecordCheck("web", false, 0)

	state := m.GetCircuitState("web")
	if state != CircuitOpen {
		t.Errorf("expected circuit open after 3 failures, got %v", state)
	}
}

func TestRecordCheck_CircuitBreakerClosesOnSuccess(t *testing.T) {
	m := NewMetrics()
	// 3 failures
	m.RecordCheck("web", false, 0)
	m.RecordCheck("web", false, 0)
	m.RecordCheck("web", false, 0)

	// Then success
	m.RecordCheck("web", true, 0)

	state := m.GetCircuitState("web")
	if state != CircuitClosed {
		t.Errorf("expected circuit closed after success, got %v", state)
	}
}

// ─── ShouldCheck tests ───

func TestShouldCheck_NewService(t *testing.T) {
	m := NewMetrics()
	if !m.ShouldCheck("new-service") {
		t.Error("expected true for new service")
	}
}

func TestShouldCheck_CircuitOpen_ProbeAfter30s(t *testing.T) {
	m := NewMetrics()
	// Open the circuit
	m.RecordCheck("web", false, 0)
	m.RecordCheck("web", false, 0)
	m.RecordCheck("web", false, 0)

	// Immediately — should NOT check (rate limited)
	if m.ShouldCheck("web") {
		t.Error("expected false immediately after opening circuit")
	}
}

func TestShouldCheck_RateLimiting(t *testing.T) {
	m := NewMetrics()
	m.RecordCheck("web", true, 0)

	// Immediately — should be rate limited (MinInterval=5s)
	if m.ShouldCheck("web") {
		t.Error("expected false due to rate limiting")
	}
}

// ─── GetSnapshot tests ───

func TestGetSnapshot_ContainsExpectedKeys(t *testing.T) {
	m := NewMetrics()
	m.RecordCheck("web", true, 50*time.Millisecond)
	m.IncrementCacheHits()

	snap := m.GetSnapshot()

	requiredKeys := []string{
		"checks_total", "checks_failed", "check_duration_s",
		"config_reloads", "cache_hits", "cache_misses",
		"active_checks", "circuit_breakers", "timestamp",
	}
	for _, key := range requiredKeys {
		if _, ok := snap[key]; !ok {
			t.Errorf("snapshot missing key: %s", key)
		}
	}
}

func TestGetSnapshot_ThreadSafe(t *testing.T) {
	m := NewMetrics()
	var wg sync.WaitGroup

	// Concurrent writes and reads
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			m.RecordCheck("svc", n%2 == 0, time.Duration(n)*time.Millisecond)
		}(i)
		go func() {
			defer wg.Done()
			_ = m.GetSnapshot()
		}()
	}
	wg.Wait()
}

// ─── Reset tests ───

func TestReset_ClearsMetrics(t *testing.T) {
	m := NewMetrics()
	m.RecordCheck("web", false, 100*time.Millisecond)
	m.IncrementCacheHits()
	m.IncrementConfigReloads()

	m.Reset()

	if len(m.ChecksTotal) != 0 {
		t.Errorf("expected empty ChecksTotal, got %d entries", len(m.ChecksTotal))
	}
	if m.CacheHits != 0 {
		t.Errorf("expected CacheHits=0, got %d", m.CacheHits)
	}
	if m.ConfigReloads != 0 {
		t.Errorf("expected ConfigReloads=0, got %d", m.ConfigReloads)
	}
}

// ─── Atomic counters tests ───

func TestAtomicCounters(t *testing.T) {
	m := NewMetrics()

	m.IncrementCacheHits()
	m.IncrementCacheHits()
	m.IncrementCacheMisses()
	m.IncrementConfigReloads()
	m.AddActiveCheck(1)
	m.AddActiveCheck(-1)

	snap := m.GetSnapshot()
	if snap["cache_hits"].(int64) != 2 {
		t.Errorf("expected cache_hits=2, got %v", snap["cache_hits"])
	}
	if snap["cache_misses"].(int64) != 1 {
		t.Errorf("expected cache_misses=1, got %v", snap["cache_misses"])
	}
	if snap["config_reloads"].(int64) != 1 {
		t.Errorf("expected config_reloads=1, got %v", snap["config_reloads"])
	}
	if snap["active_checks"].(int64) != 0 {
		t.Errorf("expected active_checks=0, got %v", snap["active_checks"])
	}
}

// ─── CircuitStateEnum tests ───

func TestCircuitStateEnum_StringRepresentation(t *testing.T) {
	m := NewMetrics()

	// Closed
	if m.GetCircuitState("x") != CircuitClosed {
		t.Error("expected CircuitClosed for unknown service")
	}

	// Force open via failures
	m.RecordCheck("x", false, 0)
	m.RecordCheck("x", false, 0)
	m.RecordCheck("x", false, 0)

	snap := m.GetSnapshot()
	circuits := snap["circuit_breakers"].(map[string]string)
	if circuits["x"] != "open" {
		t.Errorf("expected 'open' in snapshot, got %q", circuits["x"])
	}
}

// ─── Prometheus metrics tests ───

func TestGetPrometheusMetrics_ContainsExpectedMetrics(t *testing.T) {
	m := NewMetrics()
	m.RecordCheck("web", true, 50*time.Millisecond)
	m.RecordCheck("db", false, 200*time.Millisecond)
	m.IncrementCacheHits()
	m.IncrementCacheMisses()
	m.IncrementConfigReloads()

	output := m.GetPrometheusMetrics()

	required := []string{
		"homedash_config_reloads_total",
		"homedash_cache_hits_total",
		"homedash_cache_misses_total",
		"homedash_active_checks",
		"homedash_checks_total",
		"homedash_checks_failed_total",
		"homedash_check_duration_seconds",
		"homedash_circuit_breaker_state",
	}
	for _, metric := range required {
		if !strings.Contains(output, metric) {
			t.Errorf("Prometheus metrics missing: %s", metric)
		}
	}
}

func TestGetPrometheusMetrics_HasHelpAndType(t *testing.T) {
	m := NewMetrics()
	output := m.GetPrometheusMetrics()

	if !strings.Contains(output, "# HELP") {
		t.Error("Prometheus metrics missing # HELP lines")
	}
	if !strings.Contains(output, "# TYPE") {
		t.Error("Prometheus metrics missing # TYPE lines")
	}
}
