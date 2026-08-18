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
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/migrate ./cmd/migrate

# Финальный образ
FROM alpine:3.20

RUN apk --no-cache add ca-certificates tzdata wget

# Непривилегированный пользователь: процесс не должен работать от root.
RUN adduser -D -u 10001 -g gophprofile gophprofile

WORKDIR /app

COPY --from=builder /out/server /out/worker /out/migrate ./
COPY migrations ./migrations

USER 10001

EXPOSE 8080