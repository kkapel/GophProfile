# Сборка бинарников
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Слой с зависимостями кэшируется отдельно и не пересобирается при правках кода.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 даёт статический бинарник, который работает в alpine без libc.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/worker ./cmd/worker

# Финальный образ
FROM alpine:3.20

# ca-certificates нужны для HTTPS, wget — для healthcheck.
RUN apk --no-cache add ca-certificates tzdata wget

WORKDIR /app

COPY --from=builder /out/server /out/worker ./
# Миграции читаются с диска, поэтому их копируем.
# Веб-интерфейс вшит в бинарник через embed и копирования не требует.
COPY migrations ./migrations

EXPOSE 8080