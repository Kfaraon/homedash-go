# ========== BUILD STAGE ==========
FROM golang:1.21-alpine AS builder

# Установка зависимостей для сборки
RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Кэшируем загрузку модулей (слой кэшируется отдельно)
COPY go.mod go.sum ./
RUN go mod download && go mod verify

# Копируем только Go файлы для лучшего кэширования слоёв
COPY *.go ./
COPY templates/ ./templates/
COPY static/ ./static/

# Сборка с оптимизацией
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o homedash .

# ========== RUNTIME STAGE ==========
FROM alpine:3.20 AS runtime

# Минимальный набор: ca-certificates для HTTPS, wget для healthcheck
RUN apk add --no-cache ca-certificates wget tzdata

WORKDIR /app

# Копируем бинарник из builder
COPY --from=builder /app/homedash .

# Копируем шаблоны и статику
COPY --from=builder /app/templates ./templates
COPY --from=builder /app/static ./static

# Копируем конфиг (может быть переопределен через volume)
COPY --from=builder /app/config.json .

# Создаём непривилегированного пользователя
RUN addgroup -g 1000 appgroup && \
    adduser -u 1000 -G appgroup -s /bin/sh -D appuser && \
    chown -R appuser:appgroup /app

# Настройка часового пояса
ENV TZ=Europe/Moscow

# Переменные окружения
ENV PORT=5000
ENV CHECK_TIMEOUT=2s
ENV PING_TIMEOUT=1s
ENV CONFIG_FILE=config.json

EXPOSE 5000

# Health check через wget
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:5000/health || exit 1

# Запуск от непривилегированного пользователя
USER appuser

CMD ["/app/homedash"]
