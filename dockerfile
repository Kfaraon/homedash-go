FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY . .
RUN go build -o homedash .

FROM alpine:latest
RUN apk add --no-cache iputils
WORKDIR /app
COPY --from=builder /app/homedash .
COPY templates ./templates
COPY static ./static
COPY config.json .
EXPOSE 5000
CMD ["./homedash"]