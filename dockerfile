# ========== BUILD STAGE ==========
FROM golang:1.21-alpine AS builder

# Установка зависимостей для сборки
RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Кэшируем загрузку модулей
COPY go.mod ./
RUN go mod download

# Копируем исходники
COPY . .

# Сборка с оптимизацией
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o homedash .

# ========== RUNTIME STAGE ==========
FROM alpine:latest

# Установка ping для проверки доступности
RUN apk add --no-cache iputils ca-certificates tzdata

# Создание непривилегированного пользователя
RUN addgroup -g 1000 appgroup && \
    adduser -u 1000 -G appgroup -s /bin/sh -D appuser

WORKDIR /app

# Копируем бинарник из builder
COPY --from=builder /app/homedash .

# Копируем шаблоны и статику
COPY templates ./templates
COPY static ./static

# Копируем конфиг (может быть переопределен через volume)
COPY config.json .

# Настройка часового пояса
ENV TZ=Europe/Moscow

# Переменные окружения
ENV PORT=5000
ENV CHECK_TIMEOUT=2s
ENV PING_TIMEOUT=1s

EXPOSE 5000

# Health check
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:5000/health || exit 1

# Запуск от непривилегированного пользователя
USER appuser

CMD ["./homedash"]