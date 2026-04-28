# ========== BUILD STAGE ==========
FROM --platform=$BUILDPLATFORM golang:1.26-alpine3.23 AS builder
ARG TARGETOS
ARG TARGETARCH
RUN apk add --no-cache git ca-certificates
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -ldflags="-s -w" -o homedash .

# ========== RUNTIME STAGE ==========
FROM alpine:3.23.4 AS runtime
RUN apk add --no-cache ca-certificates curl wget tzdata
WORKDIR /app
COPY --from=builder /app/homedash .
COPY --from=builder /app/templates ./templates
COPY --from=builder /app/static ./static
COPY healthcheck.sh /usr/local/bin/healthcheck.sh

RUN chmod +x /usr/local/bin/healthcheck.sh
RUN addgroup -g 1000 appgroup && \
    adduser -u 1000 -G appgroup -s /bin/sh -D appuser && \
    chown -R appuser:appgroup /app

ENV TZ=Asia/Yekaterinburg
ENV PORT=5000
ENV CONFIG_FILE=/app/data/config.json
ENV ICONS_CONFIG_PATH=/app/data/icons.json
EXPOSE 5000

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/healthcheck.sh"]

USER appuser
CMD ["/app/homedash"]