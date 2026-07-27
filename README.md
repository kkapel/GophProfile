# GophProfile

Микросервис управления аватарками пользователей: приём изображений, асинхронное
создание миниатюр и выдача файлов через REST API и веб-интерфейс.

## Возможности

- Загрузка изображений (JPEG, PNG, WebP) размером до 10 МБ
- Проверка формата по сигнатуре файла, а не по расширению
- Асинхронное создание миниатюр 100×100 и 300×300
- Выдача оригинала или миниатюры нужного размера
- Мягкое удаление записи в БД и асинхронное удаление файлов из хранилища
- Веб-интерфейс: форма загрузки с превью и галерея пользователя
- Проверка работоспособности зависимостей на `/health`

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

Каждый нижний слой скрыт за интерфейсом, поэтому сервисы и обработчики
тестируются на моках без поднятия инфраструктуры.

## Быстрый старт

```bash
docker compose up --build -d
docker compose ps
```

Поднимутся пять контейнеров: PostgreSQL, MinIO, RabbitMQ, server и worker.
Миграции применяются автоматически при старте сервера, бакет создаётся
при первом подключении к хранилищу.

Точки входа:

| Адрес | Назначение |
|---|---|
| http://localhost:8080 | веб-интерфейс |
| http://localhost:8080/health | состояние компонентов |
| http://localhost:9001 | консоль MinIO (`minioadmin` / `minioadmin`) |
| http://localhost:15672 | панель RabbitMQ (`guest` / `guest`) |

Остановка:

```bash
docker compose down      # оставить данные
docker compose down -v   # удалить данные
```

## Локальный запуск

Инфраструктура — в Docker, приложение — из исходников.

```bash
docker compose up -d postgres minio rabbitmq
```

Переменные окружения (PowerShell):

```powershell
$env:GOPHPROFILE_DATABASE_URL = "postgres://gophprofile:gophprofile@localhost:5433/gophprofile?sslmode=disable"
$env:GOPHPROFILE_MINIO_ENDPOINT = "localhost:9000"
$env:GOPHPROFILE_MINIO_ACCESS_KEY = "minioadmin"
$env:GOPHPROFILE_MINIO_SECRET_KEY = "minioadmin"
$env:GOPHPROFILE_RABBITMQ_URL = "amqp://guest:guest@localhost:5672/"
```

Запуск в двух терминалах:

```bash
go run ./cmd/server
go run ./cmd/worker
```

## Конфигурация

Все параметры читаются из переменных окружения с префиксом `GOPHPROFILE_`.

| Переменная | Обязательна | По умолчанию | Описание |
|---|---|---|---|
| `GOPHPROFILE_HTTP_ADDRESS` | нет | `:8080` | адрес HTTP-сервера |
| `GOPHPROFILE_LOGGER_LEVEL` | нет | `INFO` | уровень логирования |
| `GOPHPROFILE_MIGRATIONS_PATH` | нет | `migrations` | каталог с миграциями |
| `GOPHPROFILE_DATABASE_URL` | **да** | — | DSN подключения к PostgreSQL |
| `GOPHPROFILE_MINIO_ENDPOINT` | нет | `localhost:9000` | адрес S3 API |
| `GOPHPROFILE_MINIO_ACCESS_KEY` | **да** | — | ключ доступа к хранилищу |
| `GOPHPROFILE_MINIO_SECRET_KEY` | **да** | — | секретный ключ хранилища |
| `GOPHPROFILE_MINIO_BUCKET` | нет | `avatars` | имя бакета |
| `GOPHPROFILE_MINIO_USE_SSL` | нет | `false` | использовать HTTPS |
| `GOPHPROFILE_RABBITMQ_URL` | **да** | — | строка подключения к брокеру |

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

**Идемпотентность.** Перед обработкой worker проверяет текущий статус записи
и пропускает уже обработанные аватарки. Операции хранилища идемпотентны сами
по себе: перезапись объекта и удаление несуществующего ключа безопасны.

**Повторные попытки.** До трёх попыток с экспоненциальной задержкой
(1 с, 2 с, 4 с). После исчерпания сообщение отклоняется без возврата
в очередь, чтобы не образовался бесконечный цикл обработки.

## Разработка

### Кодогенерация

```bash
sqlc generate                        # запросы к БД из SQL
go generate ./internal/api/...       # типы и роутер из OpenAPI
go generate ./internal/mocks/...     # моки интерфейсов
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
├── internal/
│   ├── api/                сгенерированные типы и роутер
│   ├── broker/             RabbitMQ: публикация и потребление
│   ├── config/             конфигурация из переменных окружения
│   ├── database/           подключение к PostgreSQL и миграции
│   ├── domain/             доменные модели и события
│   ├── handlers/           HTTP-обработчики REST и веб-интерфейса
│   ├── imageutil/          декодирование и создание миниатюр
│   ├── logger/             инициализация логгера
│   ├── mocks/              сгенерированные моки для тестов
│   ├── repository/         доступ к метаданным (sqlc)
│   ├── services/           бизнес-логика
│   ├── storage/            работа с S3-совместимым хранилищем
│   └── worker/             обработка событий брокера
├── migrations/             миграции базы данных
├── web/                    шаблоны веб-интерфейса
├── Dockerfile              multi-stage сборка обоих бинарников
└── docker-compose.yml      приложение и инфраструктура
```
# GophProfile