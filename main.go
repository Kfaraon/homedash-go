package main

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// Build metadata (set via -ldflags "-X main.version=...").
var (
	version   = "dev"
	commit    = ""
	buildTime = ""
)

// ─── Env helpers ───

// getEnv retrieves environment variable with fallback.
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// getPositiveDurationEnv parses a duration and requires it to be positive.
func getPositiveDurationEnv(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: неверный формат длительности %q: %w", key, v, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s: значение должно быть больше нуля", key)
	}
	return d, nil
}

// getPositiveIntEnv parses an integer and requires it to be positive.
func getPositiveIntEnv(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: неверное целое число %q: %w", key, v, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("%s: значение должно быть больше нуля", key)
	}
	return n, nil
}

// validatePort checks that the configured port is in the valid TCP range.
func validatePort(port string) error {
	n, err := strconv.Atoi(port)
	if err != nil {
		return fmt.Errorf("PORT: неверный номер порта %q: %w", port, err)
	}
	if n < 1 || n > 65535 {
		return fmt.Errorf("PORT: номер порта должен быть в диапазоне 1–65535")
	}
	return nil
}

// ─── Main ───

func main() {
	slog.Info("Starting homedash", "version", version, "commit", commit, "build_time", buildTime)

	app, err := NewApp()
	if err != nil {
		slog.Error("Error initializing app", "error", err)
		os.Exit(1)
	}

	if err := app.Run(); err != nil {
		slog.Error("Error running app", "error", err)
		os.Exit(1)
	}
}
