# homedash-go 🖥️

**Homedash** — легковесный дашборд для мониторинга домашних сервисов с встроенной админ-панелью. Автоматически подбирает цветные иконки с Iconify, проверяет доступность через HTTP и Ping, поддерживает горячую перезагрузку конфигурации и управление сервисами через веб-интерфейс без редактирования файлов.

## ✨ Возможности

- 🎨 **Автоподбор иконок** — цветные SVG с Iconify CDN по имени сервиса (100+ сервисов)
- 🔧 **Админ-панель** — добавление, редактирование, удаление групп и сервисов, drag & drop для изменения порядка, перемещение между группами
- 🟢 **Мониторинг** — HTTP + Ping проверка с кешированием и stale-while-revalidate
- 🔄 **Hot-reload конфигурации** — изменение `config.json` подхватывается автоматически без перезапуска
- 🌓 **Тёмная и светлая тема** — плавное переключение с сохранением в `localStorage`
- 🔤 **Шрифты** — Inter для UI, JetBrains Mono для технических элементов
- 🐳 **Docker-ready** — multi-stage сборка, health check, непривилегированный пользователь, кроссплатформенность
- ⚡ **Graceful shutdown** — корректное завершение по SIGINT/SIGTERM
- 📐 **Чистый UI** — компактные иконки, адаптивная вёрстка, двойной клик для редактирования
- 🛡️ **Circuit breaker** — автоматическая защита от повторных проверок недоступных сервисов

## 🚀 Быстрый старт

### Локальный запуск

```bash
go run main.go
```

Откройте http://localhost:5000 — главная страница сервисов  
Откройте http://localhost:5000/admin — админ-панель для управления

### Docker

```bash
docker compose up -d
```

> **Кроссплатформенная совместимость:**
> - ✅ Linux — полная поддержка (fsnotify, bind mounts)
> - ⚠️ Windows — fsnotify может не работать с bind mount (решение: Docker + WSL2 backend)
> - ✅ macOS — работает через Docker Desktop (fsnotify через polling)

## ⚙️ Конфигурация

### config.json

```json
{
  "groups": [
    {
      "name": "Внешние сервисы",
      "services": [
        { "name": "Google",    "url": "https://google.com" },
        { "name": "GitHub",    "url": "https://github.com" },
        { "name": "Cloudflare","url": "https://cloudflare.com" }
      ]
    },
    {
      "name": "Внутренние сервисы",
      "services": [
        { "name": "Nginx",  "url": "http://127.0.0.1:80",  "ip": "127.0.0.1" },
        { "name": "Redis",  "ip": "127.0.0.1" },
        { "name": "Grafana","url": "http://127.0.0.1:3000","ip": "127.0.0.1" }
      ]
    }
  ]
}
```

**Поддерживаемые форматы:**

| Формат | Описание | Пример |
|--------|----------|--------|
| **С группами** | Объект с `"groups": [...]` | Как выше |
| **Без групп** | Простой список сервисов | `[{"name": "...", "url": "..."}]` |

При формате без групп автоматически создаётся группа «Все сервисы».

### Поля сервиса

| Поле | Тип | Обязательное | Описание |
|------|-----|:------------:|----------|
| `name` | string | ✅ | Имя сервиса (по нему подбирается иконка) |
| `url` | string | ❌ | HTTP URL для проверки (минимум одно из `url`/`ip`) |
| `ip` | string | ❌ | IP-адрес для ping проверки |
| `icon` | string | ❌ | Явная иконка `prefix:name` (переопределяет автоподбор) |
| `verify_ssl` | bool | ❌ | Проверять SSL-сертификат (по умолчанию `false`) |

### Переменные окружения

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `PORT` | `5000` | Порт HTTP-сервера |
| `CHECK_TIMEOUT` | `2s` | Таймаут HTTP-запросов |
| `PING_TIMEOUT` | `1s` | Таймаут ping-проверки |
| `ADMIN_API_KEY` | _(пусто)_ | API-ключ для доступа к админ-панели |
| `ALLOWED_ORIGINS` | `*` | Список разрешённых CORS origins через запятую |
| `CONFIG_FILE` | `config.json` | Путь к файлу конфигурации |
| `IP_PROVIDERS` | `api.ipify.org,icanhazip.com,ifconfig.co/ip,ident.me` | Список fallback-провайдеров для определения публичного IP |
| `IP_CACHE_TTL` | `10m` | Время жизни кеша публичного IP |
| `TZ` | `Europe/Moscow` | Часовой пояс |

Пример:

```bash
PORT=8080 ADMIN_API_KEY=my-secret ./homedash
```

> 🔒 **Важно:** без `ADMIN_API_KEY` админ-панель отключена (возвращает 403).  
> Для доступа передавайте заголовок: `Authorization: Bearer my-secret`

### Горячая перезагрузка

Просто отредактируйте `config.json` — изменения подхватятся автоматически (с debounce 500мс). В логах появится сообщение:

```
Config.json change detected, reloading...
Config reloaded: groups=2 services=20
```

Кэш статусов сбрасывается, следующие запрос к `/api/status` выполнит свежие проверки.

> 💡 **Совет:** изменения можно вносить через админ-панель (`/admin`) — конфиг обновится автоматически.

## 📁 Структура проекта

```
homedash-go/
├── main.go              # Точка входа, env helpers
├── app.go               # App struct, роутинг, middleware, config watcher
├── handlers.go          # HTTP handlers, admin CRUD, caching, middleware
├── checks.go            # HTTP + Ping проверки, TCP connect fallback
├── config.go            # Загрузка/валидация/сохранение config.json
├── metrics.go           # Метрики, circuit breaker, worker pool
├── icons.go             # Автоподбор иконок (100+ сервисов), SVG fallback
├── types.go             # Типы данных (Service, Group, Status)
├── config_test.go       # Тесты конфига (load, validate, save)
├── metrics_test.go      # Тесты метрик и circuit breaker
├── handlers_test.go     # Тесты handlers, middleware, auth
├── icons_test.go        # Тесты иконок, цветов, CDN, кэша
├── bench_test.go        # Бенчмарки производительности
├── config.json          # Конфигурация сервисов
├── go.mod / go.sum      # Go-модуль (fsnotify)
├── dockerfile           # Multi-stage Docker (кроссплатформенный)
├── docker-compose.yml   # Docker Compose
├── .dockerignore        # Исключения для Docker
├── .env.example         # Пример переменных окружения
├── .gitignore           # Исключения для Git
├── README.md            # Документация
├── templates/
│   ├── home.html        # HTML-шаблон главной страницы
│   └── admin.html       # HTML-шаблон админ-панели
└── static/
    ├── home.css         # CSS-стили (общие + главная страница)
    └── admin.css        # CSS-стили админ-панели
```

## 🎨 Поддерживаемые иконки

Автоматически подбираются по имени сервиса из Iconify (Simple Icons + MDI). Если иконка не найдена — генерируется SVG-заглушка с первой буквой на фирменном фоне.
(Полный список: Proxmox, AdGuard, Home Assistant, Docker, Grafana, Nginx, PostgreSQL, Pi-hole, Plex, Nextcloud, GitHub, Telegram, Spotify и 100+ других)

## 🔌 API

Публичные эндпоинты: GET /, GET /api/status, GET /health, GET /api/myip, GET /api/metrics (JSON), GET /metrics (Prometheus).

Админ-панель API: полный CRUD групп/сервисов, перемещение, сортировка.

📖 Подробная спецификация: API_REFERENCE.md

## 🛠️ Технологии

| Слой | Технология |
|------|-----------|
| **Backend** | Go 1.21 (net/http, html/template, log/slog) |
| **File watch** | fsnotify (hot-reload конфига) |
| **Иконки** | Iconify CDN (Simple Icons + MDI) |
| **Frontend** | HTML + CSS + vanilla JS (без фреймворков) |
| **Шрифты** | Inter 400–700, JetBrains Mono 400–600 |
| **Контейнеры** | Docker multi-stage (alpine 3.20, кроссплатформенный) |

## 🧪 Тесты и бенчмарки

```bash
# Все тесты
go test ./...

# Подробный вывод
go test -v ./...

# Бенчмарки
go test -bench=. -benchmem

# С покрытием
go test -cover ./...
```

## 📝 Лицензия

MIT
