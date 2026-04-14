package main

import (
	"log/slog"
	"os"
	"time"
)

// ─── Env helpers ───

// getEnv retrieves environment variable with fallback
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// getDurationEnv retrieves environment variable as duration
func getDurationEnv(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

// ─── Main ───

func main() {
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
