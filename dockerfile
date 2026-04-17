# ========== BUILD STAGE ==========
FROM --platform=$BUILDPLATFORM golang:1.21-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

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
# GOOS/GOOS задаются автоматически через --platform
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -ldflags="-s -w" -o homedash .

# ========== RUNTIME STAGE ==========
FROM alpine:3.20 AS runtime

# Минимальный набор: ca-certificates для HTTPS, wget и curl для healthcheck
RUN apk add --no-cache ca-certificates wget curl tzdata

WORKDIR /app

# Копируем бинарник из builder
COPY --from=builder /app/homedash .

# Копируем шаблоны и статику
COPY --from=builder /app/templates ./templates
COPY --from=builder /app/static ./static

# Копируем конфиг в папку data
RUN mkdir -p data
COPY data/config.json data/

# Создаём скрипт healthcheck (curl как основной, wget как fallback)
RUN echo '#!/bin/sh\n\
# Healthcheck script with curl primary, wget fallback\n\
if command -v curl >/dev/null 2>&1; then\n\
    curl -f http://localhost:5000/health >/dev/null 2>&1\n\
    exit $?\n\
elif command -v wget >/dev/null 2>&1; then\n\
    wget --no-verbose --tries=1 --spider http://localhost:5000/health >/dev/null 2>&1\n\
    exit $?\n\
else\n\
    echo "No HTTP client available for healthcheck"\n\
    exit 1\n\
fi' > /usr/local/bin/healthcheck.sh && \
    chmod +x /usr/local/bin/healthcheck.sh

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
ENV CONFIG_FILE=/app/data/config.json

EXPOSE 5000

# Health check через универсальный скрипт
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD /usr/local/bin/healthcheck.sh

# Запуск от непривилегированного пользователя
USER appuser

CMD ["/app/homedash"]
