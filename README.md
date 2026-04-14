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

Иконки автоматически подбираются по имени сервиса из **Iconify** (Simple Icons + MDI). Поддержка **100+** сервисов:

| Категория | Сервисы |
|-----------|---------|
| **Виртуализация** | Proxmox, VMware, VirtualBox, Hyper-V |
| **Сеть и DNS** | AdGuard, Cloudflare, OPNsense, pfSense, MikroTik, Ubiquiti |
| **Умный дом** | Home Assistant, Homebridge, Domoticz, ioBroker |
| **Контейнеры** | Docker, Podman, Kubernetes, Portainer, Rancher |
| **Мониторинг** | Grafana, Prometheus, Datadog, Uptime Kuma, Zabbix, Netdata |
| **Веб-серверы** | Nginx, Apache, Caddy, Traefik, HAProxy |
| **Базы данных** | PostgreSQL, MySQL, MariaDB, Redis, MongoDB, Elasticsearch, InfluxDB, SQLite |
| **Безопасность** | Pi-hole, Bitwarden, Vaultwarden, WireGuard, Tailscale, Authentik, Authelia |
| **Медиа** | Plex, Jellyfin, Emby, Sonarr, Radarr, Prowlarr, Lidarr, qBittorrent, Transmission |
| **Облака и NAS** | Nextcloud, ownCloud, TrueNAS, Synology, QNAP, MinIO |
| **CI/CD и dev** | GitHub, GitLab, Gitea, Jenkins, SonarQube |
| **Коммуникации** | Telegram, Discord, Slack, Mattermost, Matrix, Zulip |
| **Прочее** | Firefox, Chrome, VS Code, Notion, Obsidian, Spotify, YouTube, Steam |

Если иконка не найдена — генерируется SVG-заглушка с первой буквой имени на фирменном фоне.

## 🔌 API

### `GET /api/status`

Возвращает статусы всех сервисов (кешируется на 3 секунды, stale-while-revalidate до 15с):

```json
{
  "services": {
    "Google": {
      "available": true,
      "http": true,
      "ping": null
    },
    "Nginx": {
      "available": false,
      "http": false,
      "ping": true
    }
  },
  "timestamp": "2026-04-10T14:53:00+05:00"
}
```

### `GET /health`

Health check endpoint (для Docker и оркестраторов):

```json
{
  "status": "ok",
  "config_ok": true,
  "cache_entries": 16,
  "groups_count": 2,
  "timestamp": "2026-04-10T14:53:00+05:00"
}
```

### `GET /metrics`

Метрики в JSON формате для фронтенда.

### `GET /metrics`

Метрики в формате Prometheus (text).

### Админ-панель API

> 🔒 Все admin endpoints требуют заголовок `Authorization: Bearer <ADMIN_API_KEY>`.  
> Без ключа возвращается `403 Forbidden`.

| Метод | Путь | Описание |
|-------|------|----------|
| `GET` | `/api/admin/groups` | Получить все группы |
| `POST` | `/api/admin/group` | Добавить группу |
| `DELETE` | `/api/admin/group` | Удалить группу |
| `PUT` | `/api/admin/group` | Переименовать группу |
| `POST` | `/api/admin/service` | Добавить сервис в группу |
| `PUT` | `/api/admin/service` | Обновить сервис |
| `DELETE` | `/api/admin/service` | Удалить сервис |
| `POST` | `/api/admin/service/move` | Переместить сервис в другую группу |
| `POST` | `/api/admin/service/reorder` | Изменить порядок сервисов |

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
