package main

import (
	"testing"
	"time"
)

// ─── Benchmarks: State access ───

func BenchmarkGetGroupsCopy(b *testing.B) {
	app := setupBenchmarkApp(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = app.GetGroupsCopy()
	}
}

func BenchmarkGetCache(b *testing.B) {
	app := setupBenchmarkApp(b)
	app.SetCache(map[string]Status{"svc1": {Available: true}})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = app.GetCache()
	}
}

func BenchmarkSetCache(b *testing.B) {
	app := setupBenchmarkApp(b)
	cache := map[string]Status{"svc1": {Available: true}, "svc2": {Available: false}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		app.SetCache(cache)
	}
}

// ─── Benchmarks: Icons ───

func BenchmarkResolveIcon(b *testing.B) {
	names := []string{"grafana", "docker", "nginx", "unknown-service"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = resolveIcon(names[i%len(names)], "")
	}
}

func BenchmarkResolveIconCDN(b *testing.B) {
	names := []string{"grafana", "docker", "nginx", "unknown-service"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = resolveIconCDN(names[i%len(names)], "")
	}
}

// ─── Benchmarks: Metrics ───

func BenchmarkMetricsRecordCheck(b *testing.B) {
	m := NewMetrics()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			m.RecordCheck("svc1", true, 50*time.Millisecond)
		}
	})
}

func BenchmarkMetricsShouldCheck(b *testing.B) {
	m := NewMetrics()
	m.RecordCheck("svc1", true, 0)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = m.ShouldCheck("svc1")
		}
	})
}

func BenchmarkMetricsGetSnapshot(b *testing.B) {
	m := NewMetrics()
	m.RecordCheck("svc1", true, 50*time.Millisecond)
	m.RecordCheck("svc2", false, 100*time.Millisecond)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.GetSnapshot()
	}
}

func BenchmarkMetricsGetPrometheusMetrics(b *testing.B) {
	m := NewMetrics()
	m.RecordCheck("svc1", true, 50*time.Millisecond)
	m.RecordCheck("svc2", false, 100*time.Millisecond)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.GetPrometheusMetrics()
	}
}

// ─── Benchmarks: Worker pool ───
// Note: BenchmarkCheckServicesParallel skipped — makes real HTTP requests
// which are slow and flaky in benchmarks. Use integration tests instead.

// ─── Helpers ───

func setupBenchmarkApp(b *testing.B) *App {
	b.Helper()
	app := &App{
		ConfigFile: "config.json",
		CacheTTL:   3 * time.Second,
		State: &AppState{
			cache: make(map[string]Status),
			stale: make(map[string]Status),
		},
		Metrics: NewMetrics(),
		Done:    make(chan struct{}),
	}
	groups, err := app.LoadGroups()
	if err != nil {
		b.Fatalf("load groups: %v", err)
	}
	app.SetGroups(groups)
	// Initialize HTTP transports for checkServicesInParallel benchmark
	initHTTPTransports()
	return app
}
