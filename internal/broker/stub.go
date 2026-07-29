// Package broker содержит работу с брокером сообщений.
package broker

import (
	"context"
	"log/slog"
)

// StubPublisher — заглушка публикации событий: только пишет в лог.
// Используется, пока не подключён RabbitMQ, и в тестах.
type StubPublisher struct{}

// NewStubPublisher создаёт заглушку публикатора событий.
func NewStubPublisher() *StubPublisher {
	return &StubPublisher{}
}

// Publish логирует событие вместо отправки в брокер.
func (p *StubPublisher) Publish(_ context.Context, routingKey string, event any, log *slog.Logger) error {
	log.Info("event published (stub)", "routing_key", routingKey, "event", event)

	return nil
}
