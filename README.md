# Homedash

[![Go Version](https://img.shields.io/badge/Go-1.23-blue.svg)](https://golang.org/)
[![License](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**Homedash** — легковесная панель мониторинга домашних сервисов с автоматическим обнаружением статуса, интеллектуальным кэшированием и энергосберегающим режимом.

## ✨ Возможности

- 🚀 **Мгновенный запуск** — работает из коробки
- 🎯 **Мониторинг HTTP и Ping** — проверка доступности сервисов
- 🔒 **SSL/TLS проверка** — поддержка самоподписанных сертификатов
- 🎨 **Адаптивный дизайн** — тёмная и светлая темы
- 🛠️ **Админ-панель** — управление сервисами через веб-интерфейс
- 🔄 **Hot reload** — автоматическая перезагрузка при изменении конфигурации
- ⚡ **Circuit Breaker** — защита от перегрузки недоступных сервисов
- 💤 **Energy-saving mode** — автоматическая пауза в режиме простоя
- 🏎️ **Worker Pool** — параллельная проверка до 20 сервисов одновременно
- 🌐 **CORS и Rate Limiting** — безопасность API
- 📱 **Mobile-friendly** — работает на любых устройствах
- 🎯 **Drag-and-drop** — сортировка сервисов перетаскиванием

## 🚀 Быстрый старт

### 1. Клонирование репозитория

```bash
git clone https://github.com/Kfaraon/homedash-go.git
cd homedash-go
```

### 2. Создание конфигурации

Создайте файл `config.json`:

```json
{
  "groups": [
    {
      "name": "Медиа сервисы",
      "services": [
        {
          "name": "Plex",
          "url": "http://192.168.1.100:32400",
          "icon": "plex",
          "verify_ssl": false
        },
        {
          "name": "Jellyfin",
          "url": "http://192.168.1.101:8096",
          "icon": "jellyfin",
          "verify_ssl": false
        }
      ]
    },
    {
      "name": "Сеть",
      "services": [
        {
          "name": "Роутер",
          "ip": "192.168.1.1",
          "icon": "router"
        }
      ]
    }
  ]
}
```

### 3. Запуск

```bash
go run .
```

Откройте браузер: **http://localhost:5000**

## 📦 Установка

### Требования

- Go 1.23 или выше
- Linux / macOS / Windows

### Сборка бинарного файла

```bash
# Linux / macOS
go build -o homedash

# Windows
go build -o homedash.exe
```

### Запуск

```bash
./homedash
```

## ⚙️ Конфигурация

### Переменные окружения

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `PORT` | `5000` | Порт HTTP сервера |
| `CONFIG_FILE` | `config.json` | Путь к файлу конфигурации |
| `CHECK_TIMEOUT` | `2s` | Таймаут HTTP проверки |
| `PING_TIMEOUT` | `1s` | Таймаут ICMP ping проверки |
| `MAX_WORKERS` | `20` | Максимальное количество параллельных проверок |
| `ADMIN_API_KEY` | `""` | API ключ для защиты админ-панели |
| `ALLOWED_ORIGINS` | `""` | Разрешённые CORS origin (через запятую) |
| `IDLE_TIMEOUT` | `5m` | Время бездействия перед переходом в спящий режим |
| `LAZY_CHECK_INTERVAL` | `30s` | Интервал проверки в активном режиме |
| `IP_PROVIDERS` | `https://api.ipify.org,https://icanhazip.com,https://ifconfig.co/ip` | Сервисы для определения внешнего IP |
| `IP_CACHE_TTL` | `10m` | Время жизни кэша внешнего IP |

### Структура config.json

```json
{
  "groups": [
    {
      "name": "Название группы",
      "services": [
        {
          "name": "Название сервиса",
          "url": "https://example.com",
          "ip": "192.168.1.1",
          "icon": "service-icon",
          "verify_ssl": true
        }
      ]
    }
  ],
  "admin": {
    "require_api_key": true
  }
}
```

**Поля сервиса:**

- `name` (обязательно) — название сервиса
- `url` (опционально) — HTTP/HTTPS URL для проверки
- `ip` (опционально) — IP адрес для ping проверки
- `icon` (опционально) — имя иконки (автоматическое определение по названию)
- `verify_ssl` (опционально, по умолчанию `true`) — проверять SSL сертификат

## 🖥️ Использование

### Главная страница

- 🟢 **Зелёный** — сервис доступен
- 🔴 **Красный** — сервис недоступен
- ⚪ **Серый** — сервис в режиме ожидания

### Админ-панель

Доступна по адресу: **http://localhost:5000/admin**

**Возможности:**
- ➕ Добавление/удаление групп и сервисов
- ✏️ Редактирование настроек
- 🔄 Drag-and-drop сортировка
- 🎨 Визуальный редактор иконок

**Защита админ-панели:**

Установите переменную окружения `ADMIN_API_KEY` для включения авторизации:

```bash
export ADMIN_API_KEY=your-secret-key
```

Затем добавляйте заголовок к запросам:

```
Authorization: Bearer your-secret-key
```

## 🔌 API

### Публичные endpoints

#### Получить статус всех сервисов

```bash
GET /api/status
```

**Ответ:**

```json
{
  "services": {
    "Plex": {
      "available": true,
      "http": true,
      "ping": null
    }
  }
}
```

#### Получить внешний IP

```bash
GET /api/myip
```

#### Health check

```bash
GET /health
```

**Ответ:**

```json
{
  "status": "ok",
  "timestamp": "2024-01-01T12:00:00Z"
}
```

### Admin API (требует авторизации)

| Метод | Endpoint | Описание |
|-------|----------|----------|
| `GET` | `/api/admin/groups` | Получить все группы |
| `POST` | `/api/admin/group` | Добавить группу |
| `PUT` | `/api/admin/group` | Переименовать группу |
| `DELETE` | `/api/admin/group` | Удалить группу |
| `POST` | `/api/admin/service` | Добавить сервис |
| `PUT` | `/api/admin/service` | Обновить сервис |
| `DELETE` | `/api/admin/service` | Удалить сервис |
| `POST` | `/api/admin/service/move` | Переместить сервис |
| `POST` | `/api/admin/service/reorder` | Изменить порядок сервисов |

**Пример добавления сервиса:**

```bash
curl -X POST http://localhost:5000/api/admin/service \
  -H "Authorization: Bearer your-secret-key" \
  -H "Content-Type: application/json" \
  -d '{
    "group_name": "Медиа сервисы",
    "service": {
      "name": "Plex",
      "url": "http://192.168.1.100:32400",
      "verify_ssl": false
    }
  }'
```

## 🐳 Docker

### Создание Dockerfile

```dockerfile
FROM golang:1.23-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o homedash .

FROM alpine:latest
RUN apk --no-cache add ca-certificates iputils

WORKDIR /root/
COPY --from=builder /app/homedash .
COPY --from=builder /app/templates ./templates
COPY --from=builder /app/static ./static

EXPOSE 5000
CMD ["./homedash"]
```

### Сборка и запуск

```bash
docker build -t homedash .
docker run -d \
  -p 5000:5000 \
  -v $(pwd)/config.json:/root/config.json \
  -e ADMIN_API_KEY=your-secret-key \
  --name homedash \
  homedash
```

## 📁 Структура проекта

```
homedash-go/
├── main.go              # Точка входа
├── app.go               # Инициализация приложения
├── checks.go            # Логика проверок сервисов
├── types.go             # Структуры данных
├── admin.go             # Обработчики админ-панели
├── config.go            # Загрузка и валидация конфигурации
├── icons.go             # Определение иконок
├── ip.go                # Определение внешнего IP
├── templates/
│   ├── home.html        # Шаблон главной страницы
│   └── admin.html       # Шаблон админ-панели
├── static/
│   ├── home.css         # Стили главной страницы
│   ├── admin.css        # Стили админ-панели
│   └── icons/           # SVG иконки
├── config.json          # Конфигурационный файл
├── go.mod               # Зависимости Go
└── README.md            # Документация
```

## 🔧 Особенности архитектуры

### Circuit Breaker

Автоматически отключает проверку сервисов после 3 неудачных попыток на 30 секунд, затем пробует снова (half-open state).

### Lazy Checking

Приложение переходит в энергосберегающий режим, если не было запросов в течение `IDLE_TIMEOUT` (по умолчанию 5 минут).

### Worker Pool

Параллельная проверка сервисов с настраиваемым количеством воркеров (по умолчанию 20).

### Hot Reload

Автоматическая перезагрузка конфигурации при изменении `config.json` с debounce 500ms.

### Кэширование

- **Активный кэш** — 15 секунд
- **Stale кэш** — 75 секунд (показывается во время обновления)

## ❓ FAQ

### Почему сервисы не проверяются?

Проверьте:
1. Корректность URL/IP в `config.json`
2. Доступность сервисов из сети, где запущен homedash
3. Логи приложения (возможно circuit breaker заблокировал проверку)

### Как добавить самоподписанный SSL сертификат?

Установите `verify_ssl: false` для конкретного сервиса:

```json
{
  "name": "My Service",
  "url": "https://self-signed.local",
  "verify_ssl": false
}
```

### Можно ли использовать без config.json?

Нет, конфигурационный файл обязателен. Но вы можете изменить его путь через переменную окружения `CONFIG_FILE`.

### Как изменить частоту проверок?

Используйте переменные окружения:
- `LAZY_CHECK_INTERVAL` — интервал между проверками
- `IDLE_TIMEOUT` — время перехода в спящий режим

### Почему админ-панель не открывается?

Если установлен `ADMIN_API_KEY`, добавьте заголовок авторизации или уберите ключ для отключения защиты.

## 🤝 Вклад в развитие

Pull requests приветствуются! Для серьёзных изменений сначала создайте issue для обсуждения.

### Разработка

```bash
# Установка зависимостей
go mod download

# Запуск в режиме разработки
go run .

# Тесты
go test ./...
```

## 📄 Лицензия

MIT License — см. файл [LICENSE](LICENSE) для деталей.

## 🙏 Благодарности

- [Go](https://golang.org/) — язык программирования
- [fsnotify](https://github.com/fsnotify/fsnotify) — отслеживание изменений файлов
- [go-ping](https://github.com/go-ping/ping) — ICMP ping библиотека

---

**Homedash** создан с ❤️ для мониторинга домашних сервисов.
