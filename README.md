# GophProfile

Микросервис управления аватарками пользователей: приём изображений, асинхронное
создание миниатюр и выдача файлов через REST API и веб-интерфейс. Сервис
полностью инструментирован: метрики, распределённая трассировка и
структурированные логи с корреляцией по трейсам.

## Возможности

- Загрузка изображений (JPEG, PNG, WebP) размером до 10 МБ
- Проверка формата по сигнатуре файла, а не по расширению
- Асинхронное создание миниатюр 100×100 и 300×300
- Выдача оригинала или миниатюры нужного размера
- Мягкое удаление записи в БД и асинхронное удаление файлов из хранилища
- Веб-интерфейс: форма загрузки с превью и галерея пользователя
- Проверка работоспособности зависимостей на `/health`
- Метрики Prometheus: RED, бизнес-показатели, состояние инфраструктуры
- Сквозная трассировка запросов через HTTP, БД, S3 и брокер сообщений
- Централизованные логи в Loki со ссылками на соответствующие трейсы

## Стек

| Компонент | Технология |
|---|---|
| Язык | Go 1.25 |
| HTTP-роутинг | chi v5 |
| База данных | PostgreSQL 16 |
| Объектное хранилище | MinIO (S3-совместимое) |
| Брокер сообщений | RabbitMQ (topic exchange) |
| Миграции | golang-migrate |
| Доступ к БД | pgx v5 + sqlc |
| Контракт API | OpenAPI 3.0 + oapi-codegen |
| Телеметрия | OpenTelemetry SDK + OTel Collector |
| Метрики | Prometheus + Node Exporter |
| Трассировка | Jaeger |
| Логи | Grafana Loki |
| Визуализация | Grafana |
| Тесты | testify + uber-go/mock |
| Контейнеризация | Docker, Docker Compose |

## Архитектура

Сервис состоит из двух независимых процессов и общей инфраструктуры.

```
                       ┌──────────────┐
   HTTP-запрос ──────► │ cmd/server   │ ──── publish ────┐
                       │ REST + Web   │                  │
                       └──────┬───────┘                  ▼
                              │                  ┌───────────────┐
                              │                  │   RabbitMQ    │
                              │                  │ avatars.      │
                              │                  │   exchange    │
                              │                  └───────┬───────┘
                              │                          │ consume
                              │                          ▼
                              │                  ┌──────────────┐
                              │                  │ cmd/worker   │
                              │                  │ ресайз, del  │
                              │                  └──────┬───────┘
                              ▼                         ▼
                       ┌──────────────────────────────────────┐
                       │   PostgreSQL (метаданные, статусы)   │
                       │   MinIO (оригиналы, миниатюры)       │
                       └──────────────────────────────────────┘
```

**Сценарий загрузки.** Сервер принимает файл, кладёт оригинал в MinIO, создаёт
запись в PostgreSQL со статусом `pending` и публикует событие `avatar.uploaded`.
Клиент сразу получает `201` — ждать обработки не нужно. Worker забирает событие,
скачивает оригинал, режет две миниатюры, заливает их обратно и переводит запись
в статус `completed`.

**Сценарий удаления.** Сервер проверяет владельца, проставляет `deleted_at`
и публикует `avatar.deleted`. Worker удаляет файлы из хранилища.

В сообщениях передаются только ключи объектов — сами файлы через брокер
не проходят (паттерн claim check).

### Слои

```
handlers → services → repository (PostgreSQL)
                    → storage    (MinIO)
                    → broker     (RabbitMQ)
```

Каждый нижний слой скрыт за интерфейсом, объявленным у потребителя, поэтому
сервисы и обработчики тестируются на моках без поднятия инфраструктуры.

### Потоки телеметрии

```
              логи, трейсы (OTLP/gRPC)         ┌────────┐
server, worker ──────────────────────► Collector ──────► Loki    (логи)
      │                                    └──────────► Jaeger  (трейсы)
      │
      └── /metrics ◄──── scrape ──── Prometheus ──────► Grafana
```

Логи и трейсы приложение **отправляет** в OpenTelemetry Collector, а метрики
Prometheus **забирает** сам, опрашивая эндпоинт `/metrics`. Grafana объединяет
все три источника в одном интерфейсе.

## Быстрый старт

```bash
docker compose up --build -d
docker compose ps
```

Поднимется одиннадцать контейнеров: приложение (server, worker), инфраструктура
(PostgreSQL, MinIO, RabbitMQ) и стек наблюдаемости (OTel Collector, Prometheus,
Node Exporter, Jaeger, Loki, Grafana). Миграции применяются автоматически при
старте сервера, бакет создаётся при первом подключении к хранилищу.

Точки входа:

| Адрес | Назначение | Учётные данные |
|---|---|---|
| http://localhost:8080 | веб-интерфейс | — |
| http://localhost:8080/health | состояние компонентов | — |
| http://localhost:8080/metrics | метрики сервера | — |
| http://localhost:3000 | Grafana: дашборды, логи, трейсы | `admin` / `admin` |
| http://localhost:9090 | Prometheus | — |
| http://localhost:16686 | Jaeger | — |
| http://localhost:9001 | консоль MinIO | `minioadmin` / `minioadmin` |
| http://localhost:15672 | панель RabbitMQ | `guest` / `guest` |

Остановка:

```bash
docker compose down      # оставить данные
docker compose down -v   # удалить данные
```

## Наблюдаемость

### Метрики

Сервер отдаёт метрики на `:8080/metrics`, worker — на `:8081/metrics`.
Prometheus опрашивает обоих каждые 15 секунд, список целей виден
на http://localhost:9090/targets.

**HTTP (RED):**

| Метрика | Тип | Описание |
|---|---|---|
| `gophprofile_http_requests_total` | counter | запросы по методу, маршруту и коду ответа |
| `gophprofile_http_request_duration_seconds` | histogram | длительность обработки запроса |

В метке `path` используется шаблон маршрута (`/api/v1/avatars/{avatar_id}`),
а не конкретный путь — иначе каждый идентификатор порождал бы отдельную
временную серию.

**Бизнес-показатели:**

| Метрика | Тип | Описание |
|---|---|---|
| `gophprofile_avatar_uploads_total` | counter | загрузки по итоговому статусу |
| `gophprofile_avatar_upload_duration_seconds` | histogram | длительность загрузки |
| `gophprofile_avatar_upload_size_bytes` | histogram | размеры загружаемых файлов |
| `gophprofile_avatar_deletes_total` | counter | удаления по итоговому статусу |
| `gophprofile_storage_bytes` | gauge | суммарный объём хранимых аватарок |

**Инфраструктура:**

| Метрика | Тип | Описание |
|---|---|---|
| `gophprofile_db_connections_total` | gauge | всего соединений в пуле |
| `gophprofile_db_connections_acquired` | gauge | занятые соединения |
| `gophprofile_db_connections_idle` | gauge | простаивающие соединения |
| `gophprofile_queue_depth` | gauge | сообщений в очереди брокера |
| `gophprofile_events_processed_total` | counter | обработанные события по типу и статусу |
| `gophprofile_thumbnail_duration_seconds` | histogram | время создания миниатюр |

Показатели пула вычисляются в момент опроса, глубина очередей и объём
хранилища обновляются фоновым сборщиком раз в 15 секунд, чтобы не нагружать
эндпоинт метрик обращениями к БД и брокеру.

Примеры запросов PromQL:

```promql
# запросов в секунду
sum(rate(gophprofile_http_requests_total[5m]))

# доля серверных ошибок
sum(rate(gophprofile_http_requests_total{status=~"5.."}[5m]))
  / sum(rate(gophprofile_http_requests_total[5m]))

# 95-й перцентиль длительности
histogram_quantile(0.95, sum(rate(gophprofile_http_request_duration_seconds_bucket[5m])) by (le))

# скорость успешных загрузок
rate(gophprofile_avatar_uploads_total{status="success"}[5m])

# накопление очереди
gophprofile_queue_depth
```

### Трассировка

Каждый HTTP-запрос порождает трейс, охватывающий оба процесса. Trace-контекст
передаётся в заголовках AMQP-сообщения, поэтому обработка в worker'е попадает
в то же дерево спанов, что и исходный запрос.

Типичный трейс загрузки аватарки:

```
POST /api/v1/avatars                      gophprofile-server
└─ avatar.upload
   ├─ storage.Upload                      MinIO
   ├─ CreateAvatar                        PostgreSQL
   └─ publish avatar.uploaded             RabbitMQ (producer)
      └─ consume avatar.uploaded          gophprofile-worker (consumer)
         ├─ GetAvatarByID
         ├─ avatar.thumbnails
         │  ├─ storage.Download
         │  └─ storage.Upload ×2
         └─ UpdateProcessingResult
```

Инструментировано:

- **HTTP** — `otelhttp` создаёт корневой спан и извлекает контекст из заголовков
- **PostgreSQL** — `otelpgx` добавляет спан на каждый SQL-запрос
- **MinIO** — спаны с `SpanKindClient` и атрибутами ключа, бакета и размера
- **RabbitMQ** — `SpanKindProducer` при публикации, `SpanKindConsumer` при чтении

Смотреть трейсы: http://localhost:16686, выбрать сервис `gophprofile-server`
и нажать **Find Traces**.

Выборка настроена на `AlwaysSample` — записываются все запросы. Для боевой
среды это заменяется на `TraceIDRatioBased`, чтобы не хранить трейсы каждого
обращения.

### Логи

Логи структурированные (JSON через `log/slog`) и уходят двумя путями: в stdout
контейнера и в OpenTelemetry Collector, откуда попадают в Loki. Дублирование
намеренное: `docker compose logs` продолжает работать при отладке.

Каждая запись, сделанная в рамках трассируемой операции, содержит `trace_id`
и `span_id`. В Grafana они превращаются в ссылку на Jaeger — из строки лога
можно провалиться в трассировку запроса.

Примеры запросов LogQL:

```logql
# все логи сервера
{service_name="gophprofile-server"}

# только ошибки обоих процессов
{service_name=~"gophprofile-.*"} | severity_text = "ERROR"

# неуспешные HTTP-ответы
{service_name="gophprofile-server"} | status >= 400

# медленные запросы
{service_name="gophprofile-server"} | duration_ms > 100

# читаемый поток запросов
{service_name="gophprofile-server"} |= "http request"
  | line_format "{{.method}} {{.path}} {{.status}} {{.duration_ms}}ms"
```

Атрибуты записи (`method`, `path`, `status`, `duration_ms`, `request_id`,
`trace_id`) хранятся как structured metadata и доступны для фильтрации без
парсера. Метками потока остаются только `service_name` и
`deployment_environment` — низкокардинальные значения, по которым Loki
строит индекс.

### Дашборды

Grafana подключает источники данных и дашборды автоматически при старте:
конфигурация лежит в `docker/grafana/provisioning`, сами дашборды —
в `docker/grafana/dashboards`.

Дашборд «GophProfile — обзор сервиса» (http://localhost:3000, папка
**GophProfile**) состоит из четырёх секций:

- **RED** — скорость запросов, доля ошибок, перцентили длительности, коды ответов
- **Бизнес-показатели** — загрузки, время создания миниатюр, объём хранилища
- **Инфраструктура** — глубина очередей, пул подключений, память, горутины
- **Логи** — ошибки и поток HTTP-запросов со ссылками на трейсы

## Локальный запуск

Инфраструктура — в Docker, приложение — из исходников.

```bash
docker compose up -d postgres minio rabbitmq otel-collector jaeger loki prometheus grafana
```

Переменные окружения (PowerShell):

```powershell
$env:GOPHPROFILE_DATABASE_URL = "postgres://gophprofile:gophprofile@localhost:5433/gophprofile?sslmode=disable"
$env:GOPHPROFILE_MINIO_ENDPOINT = "localhost:9000"
$env:GOPHPROFILE_MINIO_ACCESS_KEY = "minioadmin"
$env:GOPHPROFILE_MINIO_SECRET_KEY = "minioadmin"
$env:GOPHPROFILE_RABBITMQ_URL = "amqp://guest:guest@localhost:5672/"
$env:OTEL_EXPORTER_OTLP_ENDPOINT = "http://localhost:4317"
$env:OTEL_EXPORTER_OTLP_INSECURE = "true"
```

Запуск в двух терминалах:

```bash
go run ./cmd/server
go run ./cmd/worker
```

## Конфигурация

Параметры приложения читаются из переменных окружения с префиксом
`GOPHPROFILE_`, настройки телеметрии — из стандартных переменных `OTEL_`,
которые OpenTelemetry SDK подхватывает сам.

| Переменная | Обязательна | По умолчанию | Описание |
|---|---|---|---|
| `GOPHPROFILE_HTTP_ADDRESS` | нет | `:8080` | адрес HTTP-сервера |
| `GOPHPROFILE_METRICS_ADDRESS` | нет | `:8081` | адрес эндпоинта метрик worker'а |
| `GOPHPROFILE_LOGGER_LEVEL` | нет | `INFO` | уровень логирования |
| `GOPHPROFILE_MIGRATIONS_PATH` | нет | `migrations` | каталог с миграциями |
| `GOPHPROFILE_DATABASE_URL` | **да** | — | DSN подключения к PostgreSQL |
| `GOPHPROFILE_MINIO_ENDPOINT` | нет | `localhost:9000` | адрес S3 API |
| `GOPHPROFILE_MINIO_ACCESS_KEY` | **да** | — | ключ доступа к хранилищу |
| `GOPHPROFILE_MINIO_SECRET_KEY` | **да** | — | секретный ключ хранилища |
| `GOPHPROFILE_MINIO_BUCKET` | нет | `avatars` | имя бакета |
| `GOPHPROFILE_MINIO_USE_SSL` | нет | `false` | использовать HTTPS |
| `GOPHPROFILE_RABBITMQ_URL` | **да** | — | строка подключения к брокеру |
| `OTEL_SERVICE_NAME` | нет | — | имя сервиса в телеметрии |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | нет | `localhost:4317` | адрес OTel Collector |
| `OTEL_EXPORTER_OTLP_INSECURE` | нет | `false` | отключить TLS при экспорте |
| `OTEL_RESOURCE_ATTRIBUTES` | нет | — | дополнительные атрибуты ресурса |

Без обязательных переменных сервис не стартует и сообщает, какой именно
параметр отсутствует.

## REST API

Базовый путь — `/api/v1`. Спецификация: [`api/openapi.yaml`](api/openapi.yaml).

| Метод | Путь | Описание |
|---|---|---|
| `POST` | `/avatars` | загрузка аватарки |
| `GET` | `/avatars/{avatar_id}` | файл аватарки |
| `GET` | `/avatars/{avatar_id}/metadata` | метаданные |
| `DELETE` | `/avatars/{avatar_id}` | удаление аватарки |
| `GET` | `/users/{user_id}/avatar` | текущая аватарка пользователя |
| `DELETE` | `/users/{user_id}/avatar` | удаление текущей аватарки |
| `GET` | `/users/{user_id}/avatars` | список аватарок пользователя |

Изменяющие операции требуют заголовок `X-User-ID`. Удалять аватарку может
только её владелец, иначе возвращается `403`.

Запросы на получение файла поддерживают параметр `size` со значениями
`original`, `100x100`, `300x300`. Если миниатюра ещё не готова, отдаётся
оригинал.

### Примеры

```bash
# загрузка
curl -X POST http://localhost:8080/api/v1/avatars \
  -H "X-User-ID: user1" \
  -F "file=@photo.jpg"

# миниатюра
curl "http://localhost:8080/api/v1/avatars/<id>?size=100x100" --output thumb.jpg

# метаданные
curl http://localhost:8080/api/v1/avatars/<id>/metadata

# удаление
curl -X DELETE http://localhost:8080/api/v1/avatars/<id> -H "X-User-ID: user1"
```

Ответ на загрузку:

```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "user_id": "user1",
  "url": "/api/v1/avatars/550e8400-e29b-41d4-a716-446655440000",
  "status": "processing",
  "created_at": "2026-07-27T09:44:59Z"
}
```

## Веб-интерфейс

| Метод | Путь | Описание |
|---|---|---|
| `GET` | `/web/upload` | форма загрузки с превью |
| `POST` | `/web/upload` | обработка формы |
| `GET` | `/web/gallery/{user_id}` | галерея пользователя |

Корень `/` перенаправляет на форму загрузки. Шаблоны вшиты в бинарник
через `go:embed`, отдельного копирования файлов при развёртывании не требуется.

## Проверка работоспособности

`GET /health` опрашивает PostgreSQL, MinIO и RabbitMQ:

```json
{
  "status": "ok",
  "components": {
    "database": "ok",
    "storage": "ok",
    "broker": "ok"
  }
}
```

При недоступности любого компонента возвращается `503` и его статус
с текстом ошибки. Этот эндпоинт используется как healthcheck контейнера,
поэтому падение зависимости отражается в `docker compose ps`.

## Модель данных

```sql
CREATE TABLE avatars (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id           VARCHAR(255) NOT NULL,
    file_name         VARCHAR(255) NOT NULL,
    mime_type         VARCHAR(100) NOT NULL,
    size_bytes        BIGINT NOT NULL,
    width             INTEGER,
    height            INTEGER,
    s3_key            VARCHAR(500) NOT NULL,
    thumbnail_s3_keys JSONB NOT NULL DEFAULT '{}'::jsonb,
    upload_status     VARCHAR(50) NOT NULL DEFAULT 'uploading',
    processing_status VARCHAR(50) NOT NULL DEFAULT 'pending',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at        TIMESTAMPTZ
);

CREATE TABLE processed_events (
    event_id     UUID PRIMARY KEY,
    event_type   VARCHAR(100) NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

Индексы: по `user_id` для неудалённых записей и по паре статусов.

Раскладка объектов в хранилище:

```
avatars/{avatar_id}/original.{ext}
thumbnails/{avatar_id}/100x100.jpg
thumbnails/{avatar_id}/300x300.jpg
```

## События брокера

Exchange `avatars.exchange` типа `topic`.

| Routing key | Очередь | Обработка |
|---|---|---|
| `avatar.uploaded` | `queue.avatars.process` | создание миниатюр |
| `avatar.deleted` | `queue.avatars.delete` | удаление файлов из хранилища |

**Идемпотентность.** Каждое сообщение несёт уникальный `event_id`. После
успешной обработки worker регистрирует его в таблице `processed_events`
и пропускает повторные доставки того же события. Дополнительно проверяется
статус обработки аватарки. Операции хранилища идемпотентны сами по себе:
перезапись объекта и удаление несуществующего ключа безопасны.

**Повторные попытки.** До трёх попыток с экспоненциальной задержкой
(1 с, 2 с, 4 с). После исчерпания сообщение отклоняется без возврата
в очередь, чтобы не образовался бесконечный цикл обработки. Ошибки,
которые повтор не исправит (некорректный JSON или идентификатор),
подтверждаются сразу и только логируются.

## Разработка

### Кодогенерация

```bash
sqlc generate                        # запросы к БД из SQL
go generate ./internal/api/...       # типы и роутер из OpenAPI
go generate ./internal/mocks/...     # моки интерфейсов сервисов и обработчиков
go generate ./internal/worker/...    # моки интерфейсов worker'а
```

Сгенерированные файлы не редактируются вручную: при изменении схемы БД,
спецификации API или интерфейсов нужно повторить генерацию.

### Тесты

```bash
go test ./... -cover
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out
```

Покрыты сервисный слой, HTTP-обработчики, обработчики событий worker'а
и утилиты работы с изображениями. Внешние зависимости подменяются моками,
инфраструктура для запуска тестов не требуется.

### Статический анализ

```bash
golangci-lint run ./...
```

Набор линтеров описан в `.golangci.yml`. Те же проверки выполняются в CI
на каждый push и pull request.

## Структура проекта

```
.
├── api/                    спецификация OpenAPI
├── cmd/
│   ├── server/             HTTP-сервер
│   └── worker/             обработчик событий
├── docker/
│   ├── grafana/            источники данных и дашборды
│   ├── loki/               конфигурация хранилища логов
│   ├── otel/               конвейеры OpenTelemetry Collector
│   └── prometheus/         цели сбора метрик
├── internal/
│   ├── api/                сгенерированные типы и роутер
│   ├── broker/             RabbitMQ: публикация, потребление, проброс контекста
│   ├── config/             конфигурация из переменных окружения
│   ├── database/           подключение к PostgreSQL и миграции
│   ├── domain/             доменные модели и события
│   ├── handlers/           HTTP-обработчики и middleware
│   ├── imageutil/          декодирование и создание миниатюр
│   ├── logger/             логгер с экспортом в OpenTelemetry
│   ├── metrics/            метрики Prometheus и фоновый сборщик
│   ├── mocks/              сгенерированные моки для тестов
│   ├── repository/         доступ к метаданным (sqlc)
│   ├── services/           бизнес-логика
│   ├── storage/            работа с S3-совместимым хранилищем
│   ├── tracing/            провайдер трассировки
│   └── worker/             обработка событий брокера
├── migrations/             миграции базы данных
├── web/                    шаблоны веб-интерфейса
├── Dockerfile              multi-stage сборка обоих бинарников
└── docker-compose.yml      приложение, инфраструктура и наблюдаемость
```

## Частые проблемы

**RabbitMQ не стартует после смены мажорной версии образа**

Данные в volume несовместимы между ветками 3.x и 4.x. Удалите том
и позвольте топологии объявиться заново при следующем запуске сервера:

```bash
docker compose down
docker volume rm gophprofile_rabbitdata
docker compose up --build -d
```

**Трейсы обрываются на границе сервисов**

Проверьте, что в обоих процессах вызывается инициализация трассировки
и что worker извлекает контекст из заголовков сообщения перед созданием
спана. Признак проблемы — в Jaeger вместо одного дерева видны отдельные
трейсы `POST /api/v1/avatars` и `consume avatar.uploaded`.

**Цель Prometheus в состоянии DOWN**

Откройте http://localhost:9090/targets — в колонке Error будет причина.
Для `gophprofile-worker` типичная причина: не поднялся отдельный сервер
метрик на `:8081`.
