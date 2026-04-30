package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

// --- Circuit Breaker Manager (replaces Metrics circuit breaker functionality) ---

type CircuitStateEnum int

const (
	CircuitClosed CircuitStateEnum = iota
	CircuitOpen
	CircuitHalfOpen
)

type CircuitState struct {
	Failures    int
	LastFailure time.Time
	State       CircuitStateEnum
	LastCheck   time.Time
	MinInterval time.Duration
}

type CircuitBreakerManager struct {
	mu     sync.RWMutex
	states map[string]*CircuitState
}

func NewCircuitBreakerManager() *CircuitBreakerManager {
	return &CircuitBreakerManager{
		states: make(map[string]*CircuitState),
	}
}

func (cbm *CircuitBreakerManager) getCircuitStateLocked(name string) *CircuitState {
	cb, exists := cbm.states[name]
	if !exists {
		cb = &CircuitState{
			State:       CircuitClosed,
			MinInterval: 5 * time.Second,
		}
		cbm.states[name] = cb
	}
	return cb
}

// ShouldCheck returns true if the service should be checked (circuit breaker + rate limiting)
func (cbm *CircuitBreakerManager) ShouldCheck(name string) bool {
	now := time.Now()

	// 1) read phase
	cbm.mu.RLock()
	cb, exists := cbm.states[name]
	if !exists {
		cbm.mu.RUnlock()
		return true
	}

	// rate limit
	if now.Sub(cb.LastCheck) < cb.MinInterval {
		cbm.mu.RUnlock()
		return false
	}

	state := cb.State
	lastFailure := cb.LastFailure
	cbm.mu.RUnlock()

	// 2) decision / optional write phase
	if state == CircuitOpen {
		if now.Sub(lastFailure) <= 30*time.Second {
			return false
		}

		// время прошло — переводим в half-open
		cbm.mu.Lock()
		cb2 := cbm.getCircuitStateLocked(name)
		// повторная проверка под Lock
		if cb2.State == CircuitOpen && now.Sub(cb2.LastFailure) > 30*time.Second {
			cb2.State = CircuitHalfOpen
		}
		cbm.mu.Unlock()
	}
	return true
}

// RecordCheck updates circuit breaker state based on check result
func (cbm *CircuitBreakerManager) RecordCheck(name string, success bool) {
	cbm.mu.Lock()
	defer cbm.mu.Unlock()

	cb := cbm.getCircuitStateLocked(name)
	cb.LastCheck = time.Now()

	if success {
		cb.Failures = 0
		cb.State = CircuitClosed
	} else {
		cb.Failures++
		cb.LastFailure = time.Now()
		if cb.State == CircuitHalfOpen || cb.Failures >= 3 {
			cb.State = CircuitOpen
		}
	}
}

// GetCircuitState returns the current circuit breaker state for a service
func (cbm *CircuitBreakerManager) GetCircuitState(name string) CircuitStateEnum {
	cbm.mu.RLock()
	defer cbm.mu.RUnlock()
	if cb, exists := cbm.states[name]; exists {
		return cb.State
	}
	return CircuitClosed
}

// Reset clears all circuit breaker states
func (cbm *CircuitBreakerManager) Reset() {
	cbm.mu.Lock()
	defer cbm.mu.Unlock()
	cbm.states = make(map[string]*CircuitState)
}

// --- Service check functions ---

// checkService checks a single service
func checkService(ctx context.Context, s Service, pingTimeout time.Duration) Status {
	var httpOk, pingOk *bool

	if s.URL != "" {
		ok := checkHTTP(ctx, s.URL, s.VerifySSL)
		httpOk = &ok
	}

	if s.IP != "" {
		ok := checkPing(ctx, s.IP, pingTimeout)
		pingOk = &ok
	}

	avail := false
	switch {
	case s.URL != "" && s.IP != "":
		avail = (httpOk != nil && *httpOk) || (pingOk != nil && *pingOk)
	case s.URL != "":
		avail = httpOk != nil && *httpOk
	case s.IP != "":
		avail = pingOk != nil && *pingOk
	}

	return Status{Available: avail, HTTP: httpOk, Ping: pingOk}
}

// checkHTTP performs HTTP request with reusable client
func checkHTTP(ctx context.Context, u string, verifySSL bool) bool {
	client := getHTTPClient(verifySSL)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		slog.Debug("HTTP request failed", "url", u, "verify_ssl", verifySSL, "error", err)
		return false
	}

	resp, err := client.Do(req)
	if err != nil {
		slog.Debug("HTTP check failed", "url", u, "error", err)
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		return false
	}

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32*1024))
	resp.Body.Close()

	return resp.StatusCode < 500
}

// checkPing performs host availability check
func checkPing(ctx context.Context, ip string, pingTimeout time.Duration) bool {
	host, port := extractHostAndPort(ip)

	if port != "" {
		return tcpConnect(ctx, host, port)
	}

	if tcpConnect(ctx, host, "80") || tcpConnect(ctx, host, "443") {
		return true
	}

	return executePing(ctx, host, pingTimeout)
}

func extractHostAndPort(addr string) (host, port string) {
	if h, p, err := net.SplitHostPort(addr); err == nil {
		return h, p
	}
	return addr, ""
}

func tcpConnect(ctx context.Context, host, port string) bool {
	dialer := &net.Dialer{Timeout: 500 * time.Millisecond}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if conn != nil {
		defer conn.Close()
	}
	return err == nil
}

func executePing(ctx context.Context, ip string, pingTimeout time.Duration) bool {
	timeoutSec := int(pingTimeout.Seconds())
	if timeoutSec < 1 {
		timeoutSec = 1
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.CommandContext(ctx, "ping", "-n", "1", "-w", fmt.Sprintf("%d", timeoutSec*1000), ip)
	default:
		cmd = exec.CommandContext(ctx, "ping", "-c", "1", "-W", fmt.Sprintf("%d", timeoutSec), ip)
	}

	if err := cmd.Run(); err != nil {
		slog.Debug("Ping failed", "host", ip, "error", err)
		return false
	}
	return true
}

// --- Worker pool for parallel checks ---

func checkServicesInParallel(ctx context.Context, groups []Group, cb *CircuitBreakerManager, pingTimeout time.Duration, maxWorkers int) map[string]Status {
	type serviceTask struct {
		Svc         Service
		PingTimeout time.Duration
	}

	var tasks []serviceTask
	for _, g := range groups {
		for _, s := range g.Services {
			if cb.ShouldCheck(s.Name) {
				tasks = append(tasks, serviceTask{Svc: s, PingTimeout: pingTimeout})
			}
		}
	}

	if len(tasks) == 0 {
		return make(map[string]Status)
	}

	workers := maxWorkers
	if len(tasks) < workers {
		workers = len(tasks)
	}

	taskCh := make(chan serviceTask, len(tasks))
	resultCh := make(chan struct {
		name   string
		status Status
	}, len(tasks))
	var wg sync.WaitGroup

	for _, t := range tasks {
		taskCh <- t
	}
	close(taskCh)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range taskCh {
				st := checkService(ctx, task.Svc, task.PingTimeout)
				cb.RecordCheck(task.Svc.Name, st.Available)
				resultCh <- struct {
					name   string
					status Status
				}{task.Svc.Name, st}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	sm := make(map[string]Status)
	for res := range resultCh {
		sm[res.name] = res.status
	}
	return sm
}

// --- HTTP Transport Pool ---

var (
	transportSecure    *http.Transport
	transportInsecure  *http.Transport
	httpClientSecure   *http.Client
	httpClientInsecure *http.Client
	transportOnce      sync.Once
)

func initHTTPTransports() {
	transportOnce.Do(func() {
		transportSecure = &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     90 * time.Second,
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: false},
		}
		transportInsecure = &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     90 * time.Second,
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
		}

		httpClientSecure = &http.Client{
			Transport: transportSecure,
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return http.ErrUseLastResponse
				}
				return nil
			},
		}

		httpClientInsecure = &http.Client{
			Transport: transportInsecure,
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return http.ErrUseLastResponse
				}
				return nil
			},
		}
	})
}

func getHTTPTransport() *http.Transport {
	return transportSecure
}

func getHTTPClient(verifySSL bool) *http.Client {
	if verifySSL {
		return httpClientSecure
	}
	return httpClientInsecure
}
