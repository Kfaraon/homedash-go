# QWEN.md — homedash-go

## Project Overview

**Homedash** — легковесный дашборд для мониторинга домашних сервисов, написанный на Go 1.21. Предоставляет веб-интерфейс с автоподбором иконок (Iconify CDN), мониторинг доступности через HTTP + Ping, админ-панель с CRUD операциями и drag & drop, горячую перезагрузку конфигурации через fsnotify.

### Ключевые особенности

- **Чистый Go stdlib** — только одна внешняя зависимость (`fsnotify`), без фреймворков
- **Dependency Injection** — все зависимости через `App` struct, никаких глобальных переменных (кроме rate limiter)
- **Thread-safe** — `sync.RWMutex` для всех операций с состоянием, атомарные обновления
- **Circuit Breaker** — паттерн circuit breaker для предотвращения повторных проверок недоступных сервисов
- **Worker Pool** — конкурентная проверка сервисов с ограничением параллелизма
- **Hot-reload** — fsnotify watcher с debounce 500мс для `config.json`
- **Graceful shutdown** — корректное завершение по SIGINT/SIGTERM с timeout 5s
- **Docker-ready** — multi-stage build, healthcheck, непривилигированный пользователь (UID 1000)

### Технологии

| Слой | Технология |
|------|-----------|
| Backend | Go 1.21 (net/http, html/template, log/slog) |
| File watch | fsnotify v1.9.0 |
| Иконки | Iconify CDN (Simple Icons + MDI) |
| Frontend | HTML + CSS + vanilla JS (без фреймворков) |
| Шрифты | Inter 400–700, JetBrains Mono 400–600 |
| Контейнеры | Docker multi-stage (alpine 3.20) |

## Building and Running

### Локальная разработка

```bash
# Запуск
go run main.go

# Сборка
go build -o homedash .

# Оптимизированная сборка
go build -ldflags="-s -w" -o homedash .
```

Сервер стартует на `http://localhost:5000` (порт настраивается через `PORT`).

### Docker

```bash
docker compose up -d
```

**Кроссплатформенная совместимость:**
- ✅ Linux — полная поддержка (fsnotify, bind mounts)
- ⚠️ Windows — fsnotify может не работать с bind mount из-за различий файловых систем
  - **Решение:** Использовать Docker с WSL2 backend или скопировать `config.json` в image
- ✅ macOS — работает через Docker Desktop (fsnotify через polling)

**Переопределение порта:**
```bash
HOMEDASH_PORT=8080 docker compose up -d
```

### Тесты

```bash
# Все тесты
go test ./...

# Конкретный файл тестов
go test -v -run TestConfig config_test.go
go test -v -run TestMetrics metrics_test.go
go test -v -run TestHandlers handlers_test.go
```

### Lint

```bash
# golangci-lint (если установлен)
golangci-lint run

# Только vet
go vet ./...
```

## Architecture

### Структура файлов

```
homedash-go/
├── main.go              # Точка входа, env helpers
├── app.go               # App struct, роутинг, middleware, config watcher, rate limiter
├── handlers.go          # HTTP handlers, admin CRUD endpoints, caching, auth middleware
├── checks.go            # HTTP + Ping проверки, TCP connect fallback, HTTP transport pool
├── config.go            # Загрузка/валидация/сохранение config.json
├── metrics.go           # Метрики, circuit breaker, worker pool для проверок
├── icons.go             # Автоподбор иконок (100+ сервисов), SVG fallback
├── types.go             # Types: Service, Group, Status, AdminData
├── config_test.go       # Тесты конфига
├── metrics_test.go      # Тесты метрик и circuit breaker
├── handlers_test.go     # Тесты handlers и middleware
├── config.json          # Конфигурация сервисов
├── dockerfile           # Multi-stage Docker
├── docker-compose.yml   # Docker Compose
├── templates/
│   ├── home.html        # Главная страница
│   └── admin.html       # Админ-панель
└── static/
    └── home.css         # CSS стили
```

### Поток данных

1. **Startup**: `main()` → `NewApp()` (load config, init transports, templates) → `app.Run()`
2. **HTTP Server**: `buildRouter()` → middleware chain (CORS → MaxBytes → ContentType → Auth → RateLimit)
3. **Status Check**: `/api/status` → metrics worker pool → `checkService()` (HTTP + Ping) → cache result (TTL 3s)
4. **Config Reload**: fsnotify event → debounce 500ms → `reloadConfig()` → `SetGroups()` (atomic) → reset metrics
5. **Shutdown**: SIGINT/SIGTERM → `close(app.Done)` → `srv.Shutdown(5s)` → cleanup

### Middleware chain

```
CORS → MaxBytes (1MB) → ContentType → [Auth + RateLimit для /admin/*] → Handler
```

### Кэширование

- **TTL**: 3 секунды для `/api/status`
- **Stale cache**: возвращается если fresh cache истёк (до 5× TTL)
- **Thread-safe**: `sync.RWMutex` для всех операций с cache

### Circuit Breaker

- **Threshold**: 3 consecutive failures → circuit opens
- **Recovery**: после 30s open → half-open → 1 success → closed
- **Metrics**: записывается в `Metrics.CircuitBreaker`

## Development Conventions

### Код-стайл

- **Именование**: CamelCase для exported, camelCase для unexported
- **Комментарии**: только для exported функций/типов (godoc style)
- **Error handling**: ранний return, никаких nested if-else для ошибок
- **Мьютексы**: `RLock` для чтения, `Lock` для записи, всегда `defer Unlock`
- **Context**: все длительные операции принимают `context.Context`

### Тестирование

- Тесты в отдельных файлах: `*_test.go` рядом с исходным кодом
- Table-driven tests где возможно
- Mock через interface или test-specific structs
- Покрытие: config load/validate/save, metrics circuit breaker, handlers auth

### Конфигурация

- **config.json**: groups → services, либо flat список (fallback)
- **Env vars**: `PORT`, `CHECK_TIMEOUT`, `PING_TIMEOUT`, `ADMIN_API_KEY`, `ALLOWED_ORIGINS`
- **Hot-reload**: изменения подхватываются автоматически (debounce 500ms)

### HTTP Transport Pool

- **Reusable clients**: `httpClientSecure` / `httpClientInsecure` (singleton via `sync.Once`)
- **Keep-alive**: `MaxIdleConns: 100`, `IdleConnTimeout: 90s`
- **No client timeout**: relies on context timeout only (avoids conflict)

## Common Tasks

### Добавить новый сервис в иконки

Отредактировать `icons.go` → добавить entry в `iconMap` с `prefix:name` и `bgColor`.

### Изменить кэш TTL

В `NewApp()` → `CacheTTL: 3 * time.Second`

### Добавить новый middleware

В `buildRouter()` → добавить в chain: `handler = app.newMiddleware(handler)`

### Изменить circuit breaker threshold

В `metrics.go` → `if cb.Failures >= 3 {` (строка ~80)
