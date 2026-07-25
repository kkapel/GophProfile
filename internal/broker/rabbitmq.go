package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/kkapel/GophProfile/internal/domain"
)

// Параметры брокера.
const (
	// ExchangeName — точка обмена, куда публикуются все события аватарок.
	ExchangeName = "avatars.exchange"
	// QueueProcess — очередь задач на обработку изображений.
	QueueProcess = "avatars.process"
	// QueueDelete — очередь задач на удаление файлов.
	QueueDelete = "avatars.delete"
)

// RabbitMQ — подключение к брокеру сообщений.
type RabbitMQ struct {
	conn    *amqp.Connection
	channel *amqp.Channel
}

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

	r := &RabbitMQ{conn: conn, channel: channel}
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
// с указанным routing key.
func (r *RabbitMQ) Publish(ctx context.Context, routingKey string, event any) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

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
		},
	); err != nil {
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
