# homedash-go — Development Guide

## Quick Start

```bash
# Запуск
go run main.go

# Сборка
go build -o homedash .

# Тесты
go test ./...

# Docker
docker compose up -d
```

Сервер: `http://localhost:5000`

---

## Architecture Overview

### Core Components

| Component | File | Responsibility |
|-----------|------|----------------|
| App struct | `app.go` | DI container, routing, middleware, config watcher |
| Handlers | `handlers.go` | HTTP handlers, admin CRUD, caching, auth |
| Checks | `checks.go` | HTTP + Ping checks, TCP fallback, transport pool |
| Config | `config.go` | Load/validate/save `config.json` |
| Metrics | `metrics.go` | Counters, circuit breaker, worker pool |
| Icons | `icons.go` | Auto-icon mapping (100+ services), SVG fallback |
| Types | `types.go` | Service, Group, Status, AdminConfig structs |

### Request Flow

```
Client → CORS → ContentType → MaxBytes → [Auth + RateLimit for /admin] → Handler
```

### Key Patterns

- **No global state** — all dependencies in `App` struct (except rate limiter)
- **Thread-safe** — `sync.RWMutex` for all state operations
- **Circuit breaker** — 3 failures → open → probe after 30s → half-open → close on success
- **Worker pool** — 20 concurrent workers for service checks
- **Hot-reload** — fsnotify + 500ms debounce for `config.json`
- **Cache** — 3s TTL + stale-while-revalidate (up to 15s)

### Caching Strategy

```
Fresh cache (TTL < 3s) → return immediately
Stale cache (TTL < 15s) → return stale + refresh in background
No cache → return empty + refresh in background
```

---

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `5000` | HTTP server port |
| `CHECK_TIMEOUT` | `2s` | HTTP check timeout |
| `PING_TIMEOUT` | `1s` | Ping check timeout |
| `ADMIN_API_KEY` | `""` | Admin panel API key (empty = disabled) |
| `ALLOWED_ORIGINS` | `""` | CORS allowed origins (comma-separated, `*` if empty) |
| `CONFIG_FILE` | `config.json` | Path to config file |
| `IP_PROVIDERS` | `api.ipify.org,icanhazip.com,ifconfig.co/ip,ident.me` | Fallback providers for public IP |
| `IP_CACHE_TTL` | `10m` | Cache duration for public IP |
| `TZ` | `Europe/Moscow` | Timezone |

### config.json Structure

```json
{
  "groups": [
    {
      "name": "Infrastructure",
      "services": [
        {
          "name": "Grafana",
          "url": "http://localhost:3000",
          "ip": "127.0.0.1",
          "icon": "simple-icons:grafana",
          "verify_ssl": false
        }
      ]
    }
  ],
  "admin": {
    "require_api_key": true
  }
}
```

- `url` — HTTP check target (optional if `ip` provided)
- `ip` — Ping/TCP check target (optional if `url` provided)
- `icon` — Iconify icon format (auto-detected if omitted)
- `verify_ssl` — SSL certificate verification (default: false for backward compatibility)

---

## Testing

```bash
# All tests
go test ./...

# Specific file
go test -v -run TestConfig config_test.go

# Benchmarks
go test -bench=. -benchmem bench_test.go

# Coverage
go test -cover ./...
```

### Test Categories

| File | Coverage |
|------|----------|
| `config_test.go` | Load/validate/save config, admin config |
| `metrics_test.go` | Circuit breaker, atomic counters, Prometheus format |
| `handlers_test.go` | Auth middleware, CRUD operations, CORS, rate limiting |
| `icons_test.go` | Icon resolution, caching, CDN URL generation |
| `bench_test.go` | State access, icon resolution, metrics operations |

---

## Adding New Features

### New Service Icon

Edit `icons.go` → add entry to `iconMap`:

```go
"service-name": {"simple-icons:icon-name", "#BACKGROUND", "#ICONCOLOR"},
```

### New Middleware

Add to `buildRouter()` chain in `app.go`:

```go
handler = app.newMiddleware(handler)
```

### New Admin Endpoint

1. Add route in `buildRouter()` under `adminMux`
2. Implement handler in `handlers.go`
3. Add validation in handlers.go (use `validateName`, `validateURL`, etc.)

### Change Cache TTL

In `NewApp()` → `app.CacheTTL: 3 * time.Second`

### Change Circuit Breaker Threshold

In `metrics.go` → `RecordCheck()`:

```go
if cb.Failures >= 3 {  // Change threshold here
    cb.State = CircuitOpen
}
```

---

## Docker Notes

### Cross-platform

- ✅ Linux — full fsnotify support
- ⚠️ Windows — fsnotify may not work with bind mounts
  - **Solution:** Use WSL2 backend or copy config into image
- ✅ macOS — works via Docker Desktop (polling mode)

### Production Build

```bash
docker compose up -d
HOMEDASH_PORT=8080 docker compose up -d  # Custom port
```

---

## Code Conventions

### Naming

- `CamelCase` for exported, `camelCase` for unexported
- Early returns, no nested if-else for errors

### Mutexes

- `RLock` for reads, `Lock` for writes
- Always `defer Unlock`

### Context

- All long operations accept `context.Context`
- Timeouts via context, not client-level timeouts

### Error Handling

- Return errors early
- Log with `slog` at appropriate level (Debug/Info/Warn/Error)

---

## Dependencies

| Module | Version | Purpose |
|--------|---------|---------|
| `github.com/fsnotify/fsnotify` | v1.9.0 | File watching for hot-reload |
| `golang.org/x/sys` | v0.13.0 | Indirect (fsnotify dependency) |

No frameworks, no ORM — pure Go stdlib.

---

## Common Issues

### fsnotify not triggering on Windows

Bind mounts on Windows don't support file change notifications. Use:
- WSL2 backend for Docker
- Copy config into Docker image
- Manual restart on config change

### High memory usage

- Check worker pool size (max 20)
- Ensure cache cleanup (500 entries limit for icon cache)

### SSL verification errors

- Set `verify_ssl: false` in config for self-signed certs
- Use `http://` instead of `https://` for local services
