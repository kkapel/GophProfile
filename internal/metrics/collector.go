package metrics

import (
	"context"
	"log/slog"
	"time"
)

// QueueInspector сообщает число сообщений, ожидающих в очереди.
type QueueInspector interface {
	QueueDepth(queue string) (int, error)
}

// StorageCounter сообщает суммарный объём хранимых файлов.
type StorageCounter interface {
	TotalStorageBytes(ctx context.Context) (int64, error)
}

// Collector периодически обновляет метрики, которые нельзя получить мгновенно.
type Collector struct {
	metrics *Metrics
	queues  QueueInspector
	storage StorageCounter
	names   []string
	log     *slog.Logger
}

// NewCollector создаёт сборщик инфраструктурных метрик.
func NewCollector(m *Metrics, queues QueueInspector, storage StorageCounter,
	queueNames []string, log *slog.Logger,
) *Collector {
	return &Collector{metrics: m, queues: queues, storage: storage, names: queueNames, log: log}
}

// Run обновляет метрики с заданным интервалом до отмены контекста.
func (c *Collector) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	c.collect(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.collect(ctx)
		}
	}
}

// collect снимает текущие значения. Ошибки логируются и не прерывают работу:
// сбор метрик не должен влиять на доступность сервиса.
func (c *Collector) collect(ctx context.Context) {
	for _, name := range c.names {
		depth, err := c.queues.QueueDepth(name)
		if err != nil {
			c.log.WarnContext(ctx, "collect queue depth", "queue", name, "err", err)
			continue
		}

		c.metrics.QueueDepth.WithLabelValues(name).Set(float64(depth))
	}

	total, err := c.storage.TotalStorageBytes(ctx)
	if err != nil {
		c.log.WarnContext(ctx, "collect storage size", "err", err)
		return
	}

	c.metrics.StorageBytes.Set(float64(total))
}
