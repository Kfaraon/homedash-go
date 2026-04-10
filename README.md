# homedash-go 🖥️

**Homedash** — легковесный дашборд для мониторинга домашних сервисов. Автоматически подбирает цветные иконки с Iconify, проверяет доступность через HTTP и Ping, поддерживает горячую перезагрузку конфигурации без перезапуска.

## ✨ Возможности

- 🎨 **Автоподбор иконок** — цветные SVG с Iconify CDN по имени сервиса (100+ сервисов)
- 🟢 **Мониторинг в реальном времени** — HTTP + Ping проверка с кешированием и динамическим бейджем статуса
- 🔄 **Hot-reload конфигурации** — изменение `config.json` подхватывается автоматически без перезапуска
- 🌓 **Тёмная и светлая тема** — плавное переключение с сохранением в `localStorage`
- 🔤 **Шрифты** — Inter для UI, JetBrains Mono для технических элементов
- 🐳 **Docker-ready** — multi-stage сборка, health check, непривилегированный пользователь
- ⚡ **Graceful shutdown** — корректное завершение по SIGINT/SIGTERM
- 🔧 **Гибкая настройка** — порт, таймауты через переменные окружения
- ✅ **Валидация конфига** — проверка имён, URL/IP, дубликатов при запуске

## 🚀 Быстрый старт

### Локальный запуск

```bash
go run main.go
```

Откройте http://localhost:5000

### Docker

```bash
docker compose up -d
```

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

Пример:

```bash
PORT=8080 CHECK_TIMEOUT=5s ./homedash
```

### Горячая перезагрузка

Просто отредактируйте `config.json` — изменения подхватятся автоматически. В логах появится сообщение:

```
Watching config.json for changes...
Обнаружено изменение config.json, перезагрузка...
Конфиг перезагружен: 2 групп, 20 сервисов
```

Кэш статусов сбрасывается, следующие запрос к `/api/status` выполнит свежие проверки.

## 📁 Структура проекта

```
homedash-go/
├── main.go              # Сервер, роутинг, проверки, иконки, hot-reload
├── config.json          # Конфигурация сервисов
├── go.mod / go.sum      # Go-модуль (fsnotify)
├── dockerfile           # Multi-stage Docker образ
├── docker-compose.yml   # Docker Compose
├── .dockerignore        # Исключения для Docker
├── README.md            # Документация
├── templates/
│   └── home.html        # HTML-шаблон (Go template)
└── static/
    └── style.css        # CSS-стили
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

Возвращает статусы всех сервисов (кешируется на 3 секунды):

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
  "timestamp": "2026-04-10T14:53:00+05:00"
}
```

## 🛠️ Технологии

| Слой | Технология |
|------|-----------|
| **Backend** | Go 1.21 (net/http, html/template) |
| **File watch** | fsnotify (hot-reload конфига) |
| **Иконки** | Iconify CDN (Simple Icons + MDI) |
| **Frontend** | HTML + CSS + vanilla JS (без фреймворков) |
| **Шрифты** | Inter 400–700, JetBrains Mono 400–600 |
| **Контейнеры** | Docker (multi-stage, alpine) |

## 📝 Лицензия

MIT
