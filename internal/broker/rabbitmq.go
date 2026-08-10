package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/kkapel/GophProfile/internal/domain"
)

// Параметры брокера.
const (
	// ExchangeName — точка обмена, куда публикуются все события аватарок.
	ExchangeName = "avatars.exchange"
	// QueueProcess — очередь задач на обработку изображений.
	QueueProcess = "queue.avatars.process"
	// QueueDelete — очередь задач на удаление файлов.
	QueueDelete = "queue.avatars.delete"
)

// RabbitMQ — подключение к брокеру сообщений.
type RabbitMQ struct {
	conn    *amqp.Connection
	channel *amqp.Channel
	tracer  trace.Tracer
}

// amqpHeaders адаптирует заголовки AMQP-сообщения к интерфейсу
// propagation.TextMapCarrier, чтобы OpenTelemetry мог записать
// в них trace-контекст и прочитать его на стороне потребителя.
type amqpHeaders amqp.Table

// NewRabbitMQ подключается к брокеру и объявляет топологию:
// exchange типа topic и очереди для обработки и удаления.
func NewRabbitMQ(url string) (*RabbitMQ, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("dial rabbitmq: %w", err)
	}

	channel, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open channel: %w", err)
	}

	r := &RabbitMQ{conn: conn, channel: channel, tracer: otel.Tracer("gophprofile/broker")}
	if err := r.setupTopology(); err != nil {
		_ = r.Close()
		return nil, err
	}

	return r, nil
}

// setupTopology объявляет exchange, очереди и связи между ними.
// Операции идемпотентны: повторный запуск ничего не ломает.
func (r *RabbitMQ) setupTopology() error {
	// durable=true — топология переживёт перезапуск брокера.
	if err := r.channel.ExchangeDeclare(
		ExchangeName,
		amqp.ExchangeTopic,
		true,  // durable
		false, // autoDelete
		false, // internal
		false, // noWait
		nil,
	); err != nil {
		return fmt.Errorf("declare exchange: %w", err)
	}

	bindings := map[string]string{
		QueueProcess: domain.EventAvatarUploaded,
		QueueDelete:  domain.EventAvatarDeleted,
	}

	for queue, routingKey := range bindings {
		if _, err := r.channel.QueueDeclare(
			queue,
			true,  // durable — очередь переживёт перезапуск
			false, // autoDelete
			false, // exclusive
			false, // noWait
			nil,
		); err != nil {
			return fmt.Errorf("declare queue %q: %w", queue, err)
		}

		if err := r.channel.QueueBind(
			queue,
			routingKey,
			ExchangeName,
			false, // noWait
			nil,
		); err != nil {
			return fmt.Errorf("bind queue %q: %w", queue, err)
		}
	}

	return nil
}

// Publish сериализует событие в JSON и отправляет его в exchange
// с указанным routing key. Trace-контекст передаётся в заголовках сообщения.
func (r *RabbitMQ) Publish(ctx context.Context, routingKey string, event any) error {
	ctx, span := r.tracer.Start(ctx, "publish "+routingKey,
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.destination.name", ExchangeName),
			attribute.String("messaging.rabbitmq.routing_key", routingKey),
		),
	)
	defer span.End()

	body, err := json.Marshal(event)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "marshal failed")
		return fmt.Errorf("marshal event: %w", err)
	}

	// Записываем trace-контекст в заголовки: потребитель восстановит
	// его и продолжит тот же трейс.
	headers := amqp.Table{}
	otel.GetTextMapPropagator().Inject(ctx, amqpHeaders(headers))

	// PublishWithContext уважает отмену контекста и таймауты.
	if err := r.channel.PublishWithContext(
		ctx,
		ExchangeName,
		routingKey,
		false, // mandatory
		false, // immediate
		amqp.Publishing{
			ContentType: "application/json",
			Body:        body,
			// Persistent — сообщение сохраняется на диск и не теряется
			// при перезапуске брокера.
			DeliveryMode: amqp.Persistent,
			Headers:      headers,
		},
	); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "publish failed")
		return fmt.Errorf("publish event %q: %w", routingKey, err)
	}

	return nil
}

// Consume возвращает канал сообщений из указанной очереди.
// Используется worker-сервисом.
func (r *RabbitMQ) Consume(queue string, prefetch int) (<-chan amqp.Delivery, error) {
	// QoS ограничивает число неподтверждённых сообщений у одного потребителя:
	// без этого брокер отдаст всю очередь разом.
	if err := r.channel.Qos(prefetch, 0, false); err != nil {
		return nil, fmt.Errorf("set qos: %w", err)
	}

	deliveries, err := r.channel.Consume(
		queue,
		"",    // consumer tag — сгенерирует брокер
		false, // autoAck=false: подтверждаем вручную после обработки
		false, // exclusive
		false, // noLocal
		false, // noWait
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("consume queue %q: %w", queue, err)
	}

	return deliveries, nil
}

// Ping проверяет, что соединение с брокером живо.
func (r *RabbitMQ) Ping(_ context.Context) error {
	if r.conn == nil || r.conn.IsClosed() {
		return errors.New("rabbitmq connection is closed")
	}

	return nil
}

// Close закрывает канал и соединение.
func (r *RabbitMQ) Close() error {
	var errs []error

	if r.channel != nil {
		if err := r.channel.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close channel: %w", err))
		}
	}
	if r.conn != nil {
		if err := r.conn.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close connection: %w", err))
		}
	}

	return errors.Join(errs...)
}

// QueueDepth возвращает число сообщений, ожидающих обработки в очереди.
func (r *RabbitMQ) QueueDepth(queue string) (int, error) {
	// Пассивное объявление не создаёт очередь, а лишь запрашивает её состояние.
	q, err := r.channel.QueueDeclarePassive(queue, true, false, false, false, nil)
	if err != nil {
		return 0, fmt.Errorf("inspect queue %q: %w", queue, err)
	}

	return q.Messages, nil
}

// Get возвращает значение заголовка.
func (h amqpHeaders) Get(key string) string {
	value, ok := h[key]
	if !ok {
		return ""
	}

	str, _ := value.(string)

	return str
}

// Set записывает значение заголовка.
func (h amqpHeaders) Set(key, value string) {
	h[key] = value
}

// Keys возвращает список имён заголовков.
func (h amqpHeaders) Keys() []string {
	keys := make([]string, 0, len(h))
	for key := range h {
		keys = append(keys, key)
	}

	return keys
}

// ExtractContext восстанавливает trace-контекст из заголовков сообщения.
// Используется потребителем, чтобы продолжить трейс отправителя.
func ExtractContext(ctx context.Context, headers amqp.Table) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, amqpHeaders(headers))
}
