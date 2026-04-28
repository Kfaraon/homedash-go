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

// checkService checks a single service
func checkService(ctx context.Context, s Service, pingTimeout time.Duration) Status {
	var httpOk, pingOk *bool

	// Check HTTP
	if s.URL != "" {
		ok := checkHTTP(ctx, s.URL, s.VerifySSL)
		httpOk = &ok
	}

	// Check Ping
	if s.IP != "" {
		ok := checkPing(ctx, s.IP, pingTimeout)
		pingOk = &ok
	}

	// Determine availability
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

	// Read and discard body to allow connection reuse (keep-alive)
	// Limit to 32KB to avoid blocking on large responses
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32*1024))
	resp.Body.Close()

	return resp.StatusCode < 500
}

// checkPing performs host availability check
// Fallback: if ping is unavailable, uses TCP connect
func checkPing(ctx context.Context, ip string, pingTimeout time.Duration) bool {
	host, port := extractHostAndPort(ip)

	// If port is explicitly specified — try only it
	if port != "" {
		return tcpConnect(ctx, host, port)
	}

	// Try TCP connect on standard ports as a quick check
	if tcpConnect(ctx, host, "80") || tcpConnect(ctx, host, "443") {
		return true
	}

	// Fallback to ICMP ping
	return executePing(ctx, host, pingTimeout)
}

// extractHostAndPort extracts host and port from a string like "host:port"
func extractHostAndPort(addr string) (host, port string) {
	if h, p, err := net.SplitHostPort(addr); err == nil {
		return h, p
	}
	return addr, ""
}

// tcpConnect checks availability via TCP connection to a specific port
func tcpConnect(ctx context.Context, host, port string) bool {
	dialer := &net.Dialer{Timeout: 500 * time.Millisecond}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if conn != nil {
		defer conn.Close()
	}
	return err == nil
}

// executePing executes the system ping command
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

// ===== HTTP Transport Pool =====
var (
	transportSecure    *http.Transport
	transportInsecure  *http.Transport
	httpClientSecure   *http.Client
	httpClientInsecure *http.Client
	transportOnce      sync.Once
)

// initHTTPTransports initializes the HTTP transport pool (called once from App)
func initHTTPTransports() {
	transportOnce.Do(func() {
		transportSecure = &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     90 * time.Second,
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: false}, //nolint:gosec
		}
		transportInsecure = &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     90 * time.Second,
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
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

// getHTTPTransport returns the secure HTTP transport for maintenance
func getHTTPTransport() *http.Transport {
	return transportSecure
}

// getHTTPClient returns a reusable http.Client
func getHTTPClient(verifySSL bool) *http.Client {
	if verifySSL {
		return httpClientSecure
	}
	return httpClientInsecure
}
