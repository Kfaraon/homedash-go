package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMergeStatusesKeepsSkippedServices(t *testing.T) {
	app, _ := newTestApp(t, defaultGroups())

	app.SetCache(map[string]Status{
		"A": {Available: true},
		"B": {Available: false},
	})

	merged := app.mergeStatuses(map[string]Status{
		"A": {Available: false},
	})

	if merged["A"].Available {
		t.Fatal("checked service A was not updated")
	}
	if _, ok := merged["B"]; !ok {
		t.Fatal("skipped service B disappeared from the merged cache")
	}
}

func TestMergeStatusesEmptyCheckKeepsCache(t *testing.T) {
	app, _ := newTestApp(t, defaultGroups())
	app.SetCache(map[string]Status{"A": {Available: true}})

	merged := app.mergeStatuses(map[string]Status{})
	if len(merged) != 1 || !merged["A"].Available {
		t.Fatalf("unexpected merged cache: %+v", merged)
	}
}

func TestCircuitBreakerOpensAfterFailures(t *testing.T) {
	cb := NewCircuitBreakerManager()
	name := "svc"

	for i := 0; i < 3; i++ {
		cb.RecordCheck(name, false)
	}
	if cb.ShouldCheck(name) {
		t.Fatal("circuit should be open after 3 failures")
	}

	// Simulate 31 seconds passing to enter half-open state.
	cb.mu.Lock()
	cb.states[name].LastFailure = time.Now().Add(-31 * time.Second)
	cb.states[name].LastCheck = time.Time{}
	cb.mu.Unlock()

	if !cb.ShouldCheck(name) {
		t.Fatal("circuit should allow a probe after the cooldown")
	}
}

func TestCheckHTTP(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()

	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fail.Close()

	if !checkHTTP(t.Context(), ok.URL, false) {
		t.Fatal("expected 200 to be available")
	}
	if checkHTTP(t.Context(), fail.URL, false) {
		t.Fatal("expected 500 to be unavailable")
	}
}
