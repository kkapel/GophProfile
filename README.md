# GophProfile

Микросервис управления аватарками пользователей: приём изображений, асинхронное
создание миниатюр и выдача файлов через REST API и веб-интерфейс. Сервис
инструментирован метриками, распределённой трассировкой и структурированными
логами, разворачивается в Docker Compose для локальной разработки и в Kubernetes
через Helm Chart.

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
- Централизованные логи со ссылками на соответствующие трейсы
- Развёртывание в Kubernetes: автомасштабирование, сетевые политики, Helm Chart

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
| Метрики | Prometheus + Prometheus Operator |
| Трассировка | Jaeger |
| Логи | Grafana Loki |
| Визуализация | Grafana |
| Тесты | testify + uber-go/mock |
| Оркестрация | Kubernetes, Helm |

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

### Развёртывание в Kubernetes

```
                    ┌─────────────────────────────────────────────┐
   браузер ────────►│  Ingress (nginx)                            │
   :80              │  host: gophprofile.localhost                │
                    │  proxy-body-size: 10m                       │
                    └───────────────────┬─────────────────────────┘
                                        │
                    ┌───────────────────▼─────────────────────────┐
                    │  Service gophprofile-server  (ClusterIP)    │
                    └───────────────────┬─────────────────────────┘
                                        │
        ┌───────────────────────────────▼──────────────┐   ┌──────────────┐
        │  Deployment server                           │   │ HPA          │
        │  ┌────────┐ ┌────────┐        probes:        │◄──┤ cpu 70%      │
        │  │  pod   │ │  pod   │  startup/live/ready   │   │ mem 80%      │
        │  └────────┘ └────────┘  non-root, RO rootfs  │   │ 1..5 реплик  │
        └───────────────┬──────────────────────────────┘   └──────────────┘
                        │
        ┌───────────────▼──────────────┐    ┌─────────────────────────────┐
        │  Deployment worker           │    │  Job migrate (Helm hook)    │
        │  ┌────────┐                  │    │  pre-upgrade, ждёт БД       │
        │  │  pod   │  metrics :8081   │    └─────────────────────────────┘
        │  └────────┘                  │
        └───────────────┬──────────────┘
                        │
   ┌────────────────────▼─────────────────────────────────────────┐
   │  StatefulSet + PVC                                           │
   │  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐        │
   │  │ postgres-0   │  │  minio-0     │  │ rabbitmq-0   │        │
   │  └──────────────┘  └──────────────┘  └──────────────┘        │
   └──────────────────────────────────────────────────────────────┘

   Namespace gophprofile закрыт NetworkPolicy: снаружи допускаются
   только ingress-контроллер и Prometheus, исходящий трафик наружу
   запрещён. ServiceMonitor в namespace monitoring собирает метрики
   с /metrics обоих процессов.
```

### Потоки телеметрии

```
              логи, трейсы (OTLP/gRPC)         ┌────────┐
server, worker ──────────────────────► Collector ──────► Loki    (логи)
      │                                    └──────────► Jaeger  (трейсы)
      │
      └── /metrics ◄──── scrape ──── Prometheus ──────► Grafana
```

Логи и трейсы приложение **отправляет** в OpenTelemetry Collector, а метрики
Prometheus **забирает** сам, опрашивая эндпоинт `/metrics`.

В Kubernetes развёрнута только часть стека: Prometheus Operator собирает метрики
через `ServiceMonitor`, а Collector, Jaeger и Loki остаются в docker-compose для
локальной разработки. При запуске в кластере приложение пишет в логи ошибки
экспорта телеметрии — это ожидаемо и на работу не влияет.

## Быстрый старт: Docker Compose

```bash
docker compose up --build -d
docker compose ps
```

Поднимется одиннадцать контейнеров: приложение (server, worker), инфраструктура
(PostgreSQL, MinIO, RabbitMQ) и стек наблюдаемости (OTel Collector, Prometheus,
Node Exporter, Jaeger, Loki, Grafana).

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

## Развёртывание в Kubernetes

### Требования

- Kubernetes 1.29 или новее (проверялось на k3s в Rancher Desktop)
- Helm 3
- Ingress-контроллер nginx
- Prometheus Operator, если нужен сбор метрик через `ServiceMonitor`

Подготовка кластера:

```bash
# Ingress-контроллер
helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx
helm install ingress-nginx ingress-nginx/ingress-nginx \
  --namespace ingress-nginx --create-namespace

# Prometheus Operator и Grafana
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm install monitoring prometheus-community/kube-prometheus-stack \
  --namespace monitoring --create-namespace \
  -f k8s/observability/kube-prometheus-values.yaml
```

### Установка через Helm

```bash
# Образ собирается локально и берётся кластером напрямую.
docker build -t gophprofile:1.0.0 .

kubectl apply -f k8s/namespace.yaml

helm install gophprofile ./charts/gophprofile \
  --namespace gophprofile \
  -f ./charts/gophprofile/values-dev.yaml \
  --wait --timeout 5m
```

Проверка:

```bash
kubectl get pods -n gophprofile
curl http://gophprofile.localhost/health
```

Если домен не резолвится, добавьте в файл hosts:

```
127.0.0.1 gophprofile.localhost
```

### Обновление и откат

```bash
helm upgrade gophprofile ./charts/gophprofile \
  -f ./charts/gophprofile/values-dev.yaml --wait

helm history gophprofile
helm rollback gophprofile 3
```

Перед обновлением выполняется хук `pre-upgrade`: Job запускает бинарник
`migrate`, который применяет миграции. Если они не прошли, обновление
прерывается и поды со старой версией продолжают работать.

На первой установке хук не выполняется: StatefulSet базы создаётся после хуков,
подключаться ещё некуда. Там миграции накатывает сам сервер при старте.

Вместе с Job'ом как хук создаётся отдельная `NetworkPolicy` с меньшим весом —
иначе под миграций попадает под правило `default-deny` и не может разрешить
даже имя базы. Сам Job начинается с `initContainer`, который ждёт, пока
PostgreSQL начнёт принимать подключения: на каждой попытке под создаётся
заново и получает новый адрес, а сетевые правила для него прошиваются
не мгновенно.

### Values для окружений

| Файл | Назначение |
|---|---|
| `values.yaml` | значения по умолчанию |
| `values-dev.yaml` | локальная разработка: зависимости в кластере, отладочные логи, одна реплика |
| `values-prod.yaml` | боевая среда: управляемые зависимости, внешний Secret, TLS, три реплики, выборка трейсов 10% |

Посмотреть, во что разворачиваются шаблоны, не устанавливая:

```bash
helm lint ./charts/gophprofile
helm template gophprofile ./charts/gophprofile -f ./charts/gophprofile/values-prod.yaml
```

### Сырые манифесты

В `k8s/` лежат те же ресурсы без шаблонизации — с них начиналась работа,
и они пригодятся, если Helm недоступен:

```bash
kubectl apply -f k8s/namespace.yaml
kubectl apply -f k8s/dependencies/
kubectl apply -f k8s/app/
kubectl apply -f k8s/observability/servicemonitor.yaml
```

Одновременно с чартом их применять не следует: имена ресурсов совпадают
частично, и два комплекта начнут конфликтовать за Ingress.

### Масштабирование

`HorizontalPodAutoscaler` управляет числом реплик сервера по загрузке
процессора и памяти. Проценты считаются от `requests`, а не от limits, —
без заданных `resources.requests` метрики остаются в состоянии `unknown`
и масштабирование не работает.

```bash
kubectl get hpa -n gophprofile
kubectl describe hpa gophprofile-server -n gophprofile
```

Проверить под нагрузкой:

```bash
kubectl run load-test --rm -it --image=busybox --restart=Never -- \
  sh -c "while true; do wget -q -O- http://gophprofile-server/health > /dev/null; done"
```

Окна стабилизации асимметричны: разворачивание за 30 секунд, сворачивание
за 5 минут — иначе на пилообразной нагрузке поды пересоздавались бы постоянно.

Воркер по процессору масштабировать бессмысленно: он большую часть времени ждёт
сообщений. Правильная метрика — глубина очереди, для неё понадобился бы
Prometheus Adapter.

### Пробы

| Проба | Эндпоинт | Назначение |
|---|---|---|
| `startupProbe` | `/metrics` | даёт время на запуск и миграции, до её успеха остальные не выполняются |
| `livenessProbe` | `/metrics` | живость процесса; перезапускает зависший под |
| `readinessProbe` | `/health` | доступность зависимостей; выводит под из балансировки |

Liveness намеренно не проверяет `/health`: при недоступной базе перезапуск пода
не помогает и лишь усугубляет ситуацию. Такой под должен быть выведен из
балансировки, но не перезапущен — за это отвечает readiness.

### Безопасность

- контейнеры работают от пользователя `10001`, `runAsNonRoot` не даст запустить образ от root
- корневая файловая система смонтирована только для чтения, для временных файлов подключён `emptyDir` в `/tmp`
- все привилегии Linux сброшены, повышение запрещено
- отдельный `ServiceAccount` без ролей, токен API не монтируется
- `NetworkPolicy` закрывает namespace: входящий трафик только от ingress-контроллера и Prometheus, исходящий наружу запрещён

Секреты хранятся в объектах `Secret`, то есть закодированными в base64 — это не
шифрование. В боевой среде их выносят во внешнее хранилище: чарт поддерживает
`secrets.existingSecret`, куда подставляется секрет, созданный, например,
External Secrets Operator или Vault.

### Graceful shutdown

Оба процесса обрабатывают SIGTERM: сервер перестаёт принимать соединения
и дожидается текущих запросов, воркер завершает обработку текущего сообщения.

У сервера дополнительно задан `preStop` с паузой в 5 секунд. Удаление пода
из балансировки и отправка SIGTERM происходят параллельно, и без паузы часть
запросов успевала бы прийти в уже завершающийся процесс.

`terminationGracePeriodSeconds` — 30 секунд у сервера и 60 у воркера: обработка
изображения занимает заметно больше времени, чем HTTP-запрос.

## Наблюдаемость

### Метрики

Сервер отдаёт метрики на `:8080/metrics`, worker — на `:8081/metrics`.
В Kubernetes их обнаруживает Prometheus Operator через `ServiceMonitor`,
в docker-compose — статический конфиг Prometheus.

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

Примеры запросов PromQL:

```promql
# запросов в секунду
sum(rate(gophprofile_http_requests_total[5m]))

# доля серверных ошибок
sum(rate(gophprofile_http_requests_total{status=~"5.."}[5m]))
  / sum(rate(gophprofile_http_requests_total[5m]))

# 95-й перцентиль длительности
histogram_quantile(0.95, sum(rate(gophprofile_http_request_duration_seconds_bucket[5m])) by (le))

# накопление очереди
gophprofile_queue_depth
```

### Трассировка

Каждый HTTP-запрос порождает трейс, охватывающий оба процесса. Trace-контекст
передаётся в заголовках AMQP-сообщения, поэтому обработка в worker'е попадает
в то же дерево спанов, что и исходный запрос.

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

Доля записываемых трейсов задаётся параметром `GOPHPROFILE_TRACE_SAMPLE_RATIO`.
Сэмплер обёрнут в `ParentBased`, поэтому при доле меньше единицы трейс
не разрывается между сервисами: потребитель наследует решение отправителя.

### Логи

Логи структурированные (JSON через `log/slog`) и уходят двумя путями: в stdout
контейнера и в OpenTelemetry Collector. Дублирование намеренное: `kubectl logs`
и `docker compose logs` продолжают работать при отладке.

Каждая запись, сделанная в рамках трассируемой операции, содержит `trace_id`
и `span_id`. В Grafana они превращаются в ссылку на Jaeger.

```logql
{service_name=~"gophprofile-.*"} | severity_text = "ERROR"
{service_name="gophprofile-server"} | status >= 400
{service_name="gophprofile-server"} | duration_ms > 100
```

### Дашборды

Grafana подключает источники данных и дашборды автоматически при старте:
конфигурация в `docker/grafana/provisioning`, дашборды — в
`docker/grafana/dashboards`.

Дашборд «GophProfile — обзор сервиса» состоит из четырёх секций: RED-метрики,
бизнес-показатели, инфраструктура и логи со ссылками на трейсы.

При развёртывании в Kubernetes через `kube-prometheus-stack` дополнительно
доступны готовые дашборды по кластеру — потребление ресурсов узлами и подами.

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
`GOPHPROFILE_`, настройки телеметрии — из стандартных переменных `OTEL_`.

| Переменная | Обязательна | По умолчанию | Описание |
|---|---|---|---|
| `GOPHPROFILE_HTTP_ADDRESS` | нет | `:8080` | адрес HTTP-сервера |
| `GOPHPROFILE_METRICS_ADDRESS` | нет | `:8081` | адрес эндпоинта метрик worker'а |
| `GOPHPROFILE_LOGGER_LEVEL` | нет | `INFO` | уровень логирования |
| `GOPHPROFILE_MIGRATIONS_PATH` | нет | `migrations` | каталог с миграциями |
| `GOPHPROFILE_TRACE_SAMPLE_RATIO` | нет | `1.0` | доля записываемых трейсов, от 0 до 1 |
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

Без обязательных переменных сервис не стартует и сообщает, какой именно
параметр отсутствует.

В Kubernetes несекретные значения приходят из `ConfigMap`, секреты — из
`Secret`, оба подключаются через `envFrom`.

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
curl -X POST http://gophprofile.localhost/api/v1/avatars \
  -H "X-User-ID: user1" \
  -F "file=@photo.jpg"

# миниатюра
curl "http://gophprofile.localhost/api/v1/avatars/<id>?size=100x100" --output thumb.jpg

# метаданные
curl http://gophprofile.localhost/api/v1/avatars/<id>/metadata

# удаление
curl -X DELETE http://gophprofile.localhost/api/v1/avatars/<id> -H "X-User-ID: user1"
```

## Веб-интерфейс

| Метод | Путь | Описание |
|---|---|---|
| `GET` | `/web/upload` | форма загрузки с превью |
| `POST` | `/web/upload` | обработка формы |
| `GET` | `/web/gallery/{user_id}` | галерея пользователя |

Корень `/` перенаправляет на форму загрузки. Шаблоны вшиты в бинарник
через `go:embed`.

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
с текстом ошибки. Эндпоинт используется как readiness-проба в Kubernetes
и healthcheck контейнера в docker-compose.

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
и пропускает повторные доставки. Дополнительно проверяется статус обработки
аватарки. Операции хранилища идемпотентны сами по себе.

**Повторные попытки.** До трёх попыток с экспоненциальной задержкой
(1 с, 2 с, 4 с). После исчерпания сообщение отклоняется без возврата
в очередь. Ошибки, которые повтор не исправит (некорректный JSON или
идентификатор), подтверждаются сразу и только логируются.

## Разработка

### Кодогенерация

```bash
sqlc generate                        # запросы к БД из SQL
go generate ./internal/api/...       # типы и роутер из OpenAPI
go generate ./internal/mocks/...     # моки интерфейсов сервисов и обработчиков
go generate ./internal/worker/...    # моки интерфейсов worker'а
```

Сгенерированные файлы не редактируются вручную.

### Тесты

```bash
go test ./... -cover
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out
```

Покрыты сервисный слой, HTTP-обработчики, обработчики событий worker'а
и утилиты работы с изображениями. Внешние зависимости подменяются моками.

### Статический анализ

```bash
golangci-lint run ./...
```

Набор линтеров описан в `.golangci.yml`. Те же проверки выполняются в CI.

## Структура проекта

```
.
├── api/                    спецификация OpenAPI
├── charts/gophprofile/     Helm Chart
│   ├── Chart.yaml
│   ├── values.yaml         значения по умолчанию
│   ├── values-dev.yaml     локальная разработка
│   ├── values-prod.yaml    боевая среда
│   └── templates/          шаблоны ресурсов и хук миграций
├── cmd/
│   ├── server/             HTTP-сервер
│   ├── worker/             обработчик событий
│   └── migrate/            применение миграций (Helm-хук)
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
├── k8s/                    сырые манифесты Kubernetes
│   ├── namespace.yaml
│   ├── app/                приложение
│   ├── dependencies/       PostgreSQL, MinIO, RabbitMQ
│   └── observability/      ServiceMonitor и values для kube-prometheus-stack
├── migrations/             миграции базы данных
├── web/                    шаблоны веб-интерфейса
├── Dockerfile              multi-stage сборка трёх бинарников
└── docker-compose.yml      приложение, инфраструктура и наблюдаемость
```

## Частые проблемы

**RabbitMQ не стартует после смены мажорной версии образа**

Данные в volume несовместимы между ветками 3.x и 4.x:

```bash
docker compose down
docker volume rm gophprofile_rabbitdata
docker compose up --build -d
```

**Поды падают при первом запуске с `connection refused`**

Приложение стартовало раньше базы. Kubernetes перезапускает под, вторая попытка
проходит успешно — в статусе остаётся `RESTARTS 1`. Ожидаемое поведение;
при желании убирается `initContainer`, ожидающим готовности PostgreSQL.

**Job миграций не может подключиться к базе**

Смотрите на текст ошибки. `bad address` или `name resolver error` означает,
что не работает DNS: правило `default-deny` перекрывает исходящий трафик всем
подам namespace, включая временные Job'ы, и политика для компонента `migrate`
должна создаваться как хук — обычный ресурс применяется уже после хуков.
`connection refused` при живой базе означает, что под не дождался прошивки
сетевых правил, и лечится ожиданием в `initContainer`.

**Не применяется изменение размера тома у зависимостей**

```
StatefulSet.apps is invalid: spec: Forbidden: updates to statefulset spec
for fields other than 'replicas', ... are forbidden
```

`volumeClaimTemplates` после создания StatefulSet неизменяемы. Либо верните
в values прежний размер, либо удалите объект, сохранив поды и тома:

```bash
kubectl delete statefulset gophprofile-minio -n gophprofile --cascade=orphan
```

Следующий `helm upgrade` создаст StatefulSet заново и подхватит существующий под.

**Helm отказывается устанавливать чарт из-за существующего ресурса**

Ресурс был создан через `kubectl apply` и не принадлежит релизу. Удалите
сырые манифесты перед установкой чарта.

**`kubectl` показывает пустой список**

Проверьте текущий namespace:

```bash
kubectl config set-context --current --namespace=gophprofile
```

**Трейсы обрываются на границе сервисов**

Убедитесь, что инициализация трассировки вызывается в обоих процессах
и что worker извлекает контекст из заголовков сообщения перед созданием спана.
