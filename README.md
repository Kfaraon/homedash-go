# homedash-go 🖥️

> Легковесный дашборд для мониторинга домашних сервисов на Go

**Homedash-go** — это self-hosted веб-приложение для визуализации состояния ваших сервисов. Поддерживает проверку доступности через HTTP и Ping, автоматический подбор иконок, админ-панель для управления конфигурацией и горячую перезагрузку без перезапуска.

---

## ✨ Ключевые возможности

### 🔍 Мониторинг
- **Двойная проверка**: HTTP-запросы + ICMP ping (с TCP-fallback на порты 80/443)
- **Circuit Breaker**: автоматическая пауза проверок при 3+ последовательных ошибках, восстановление через 30с
- **Worker Pool**: параллельные проверки с настраиваемым лимитом воркеров (`MAX_WORKERS`)
- **Stale-while-revalidate**: возврат закэшированных данных при фоновом обновлении

### 🎨 Визуализация
- **Автоподбор иконок**: 100+ предустановленных сервисов через Iconify CDN (Simple Icons + MDI)
- **Умные заглушки**: SVG с первой буквой сервиса, если иконка не найдена
- **Пастельные цвета**: детерминированная генерация фона по имени сервиса (Golden Angle hashing)
- **Тёмная/светлая тема**: переключение с сохранением в `localStorage`

### ⚙️ Управление
- **Веб-админка**: CRUD для групп и сервисов, drag&drop сортировка, перемещение между группами
- **Hot-reload**: изменение `config.json` подхватывается автоматически (debounce 500мс)
- **Атомарное сохранение**: запись конфига через temp-file + rename для целостности
- **API-аутентификация**: Bearer-токен через `ADMIN_API_KEY` для защиты админ-эндпоинтов

### 🛡️ Надёжность
- **Graceful shutdown**: корректная обработка SIGINT/SIGTERM с таймаутом 5с
- **Health check**: эндпоинт `/health` с проверкой состояния конфига и кэша
- **Rate limiting**: token bucket (20 burst, 10/sec refill) для админ-API
- **CORS middleware**: гибкая настройка разрешённых origins

### 🐳 Контейнеризация
- **Multi-stage Docker**: сборка в golang:alpine, запуск в минимальном alpine:runtime
- **Non-root user**: запуск от `appuser:appgroup` (UID/GID 1000)
- **Cross-platform**: поддержка linux/amd64, linux/arm64 через BUILDPLATFORM
- **Healthcheck**: встроенный скрипт проверки доступности

---

## 🚀 Быстрый старт

### Локально (требует Go 1.21+)
```bash
# Клонирование
git clone https://github.com/Kfaraon/homedash-go
cd homedash-go

# Запуск
go run .

# Доступ:
# • Дашборд: http://localhost:5000
# • Админка:  http://localhost:5000/admin (требует ADMIN_API_KEY)

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
        { "name": "GitHub", "url": "https://github.com" },
        { "name": "Cloudflare", "url": "https://cloudflare.com", "verify_ssl": true }
      ]
    },
    {
      "name": "Локальные",
      "services": [
        { "name": "Nginx", "url": "http://192.168.1.10", "ip": "192.168.1.10" },
        { "name": "Redis", "ip": "192.168.1.10:6379" }
      ]
    }
  ],
  "admin": {
    "require_api_key": true
  }
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
| `ADMIN_API_KEY` | _(пусто)_ | Секретный ключ для админ-API (Bearer-токен) |
| `ALLOWED_ORIGINS` | `*` | CORS origins через запятую |
| `CONFIG_FILE` | `config.json` | Путь к файлу конфигурации |
| `ICONS_CONFIG_PATH` | `data/icons.json` | Путь к конфигу иконок |
| `IP_PROVIDERS` | `https://api.ipify.org,https://icanhazip.com,https://ifconfig.co/ip,https://ident.me` | Fallback-провайдеры для определения публичного IP |
| `IP_CACHE_TTL` | `10m` | TTL кэша публичного IP |
| `MAX_WORKERS` | `20` | Макс. количество параллельных проверок |
| `IDLE_TIMEOUT` | `5m` | Период неактивности перед паузой проверок |
| `TZ` | `Asia/Yekaterinburg` | Часовой пояс для логов |

Пример:

```bash
PORT=8080 ADMIN_API_KEY=my-secret ./homedash
```

> 🔒 **Важно:** без `ADMIN_API_KEY` админ-панель отключена (возвращает 403).  
> Для доступа передавайте заголовок: `Authorization: Bearer my-secret`

## 📁 Структура проекта

```
.
├── main.go              # Точка входа, инициализация, graceful shutdown
├── app.go               # App struct: роутинг, middleware, config watcher, cache
├── handlers.go          # HTTP handlers: домашняя страница, API, админка, CORS
├── checks.go            # Проверки: HTTP, Ping, TCP-fallback, circuit breaker, worker pool
├── config.go            # Загрузка/валидация/атомарное сохранение config.json
├── icons.go             # IconResolver: автоподбор иконок, цвета, CDN, SVG-fallback
├── types.go             # Типы данных: Service, Group, Status, AdminConfig, IPCache
├── lru_cache.go         # Thread-safe LRU-кэш с TTL (опциональная замена простого кэша)
├── metrics.go           # Прометеус-метрики, circuit breaker состояния
├── templates/
│   ├── home.html        # Шаблон дашборда (с функциями иконок)
│   └── admin.html       # Шаблон админ-панели (vanilla JS)
├── static/
│   ├── home.css         # Стили дашборда (адаптив, темы, анимации)
│   └── admin.css        # Стили админки (drag&drop, модальные окна)
├── dockerfile           # Multi-stage сборка, non-root, healthcheck
├── docker-compose.yml   # Готовая конфигурация для запуска
├── healthcheck.sh       # Скрипт проверки доступности для Docker
└── data/
    └── icons.json       # Конфигурация иконок (категории, алиасы, fallback)
```

## 🎨 Поддерживаемые иконки

Автоматически подбираются по имени сервиса из Iconify (Simple Icons + MDI). Если иконка не найдена — генерируется SVG-заглушка с первой буквой на фирменном фоне.
(Полный список: Proxmox, AdGuard, Home Assistant, Docker, Grafana, Nginx, PostgreSQL, Pi-hole, Plex, Nextcloud, GitHub, Telegram, Spotify и 100+ других)

## 📝 Лицензия

MIT
