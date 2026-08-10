// Package metrics содержит метрики приложения для Prometheus.
package metrics

import (
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics — набор метрик приложения и их реестр.
type Metrics struct {
	registry *prometheus.Registry

	// Метрики HTTP-слоя: rate, errors, duration.
	HTTPRequestsTotal   *prometheus.CounterVec
	HTTPRequestDuration *prometheus.HistogramVec

	// Бизнес-метрики сервиса аватарок.
	AvatarUploadsTotal   *prometheus.CounterVec
	AvatarUploadDuration *prometheus.HistogramVec
	AvatarUploadSize     prometheus.Histogram
	AvatarDeletesTotal   *prometheus.CounterVec

	// Метрики асинхронной обработки.
	ProcessedEventsTotal *prometheus.CounterVec
	ThumbnailDuration    prometheus.Histogram

	// QueueDepth — число сообщений, ожидающих обработки.
	QueueDepth *prometheus.GaugeVec

	// StorageBytes — суммарный объём хранимых аватарок.
	StorageBytes prometheus.Gauge
}

// New создаёт набор метрик с собственным реестром.
func New() *Metrics {
	registry := prometheus.NewRegistry()

	// Собственный реестр не содержит стандартных коллекторов,
	// поэтому регистрируем их явно.
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	factory := promauto.With(registry)

	return &Metrics{
		registry: registry,

		HTTPRequestsTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gophprofile_http_requests_total",
				Help: "Total number of HTTP requests",
			},
			[]string{"method", "path", "status"},
		),

		HTTPRequestDuration: factory.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "gophprofile_http_request_duration_seconds",
				Help:    "HTTP request duration in seconds",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"method", "path"},
		),

		AvatarUploadsTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gophprofile_avatar_uploads_total",
				Help: "Total number of avatar uploads",
			},
			[]string{"status"},
		),

		AvatarUploadDuration: factory.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "gophprofile_avatar_upload_duration_seconds",
				Help:    "Avatar upload duration in seconds",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"status"},
		),

		AvatarUploadSize: factory.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "gophprofile_avatar_upload_size_bytes",
				Help:    "Size of uploaded avatar files in bytes",
				Buckets: prometheus.ExponentialBuckets(1024, 4, 8),
			},
		),

		AvatarDeletesTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gophprofile_avatar_deletes_total",
				Help: "Total number of avatar deletions",
			},
			[]string{"status"},
		),

		ProcessedEventsTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gophprofile_events_processed_total",
				Help: "Total number of processed broker events",
			},
			[]string{"event_type", "status"},
		),

		ThumbnailDuration: factory.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "gophprofile_thumbnail_duration_seconds",
				Help:    "Thumbnail generation duration in seconds",
				Buckets: prometheus.DefBuckets,
			},
		),

		QueueDepth: factory.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "gophprofile_queue_depth",
				Help: "Number of messages waiting in the queue",
			},
			[]string{"queue"},
		),

		StorageBytes: factory.NewGauge(
			prometheus.GaugeOpts{
				Name: "gophprofile_storage_bytes",
				Help: "Total storage used by avatars in bytes",
			},
		),
	}
}

// Handler возвращает HTTP-обработчик, отдающий метрики в формате Prometheus.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// RegisterGaugeFunc регистрирует метрику, значение которой вычисляется в момент сбора.
func (m *Metrics) RegisterGaugeFunc(name, help string, labels prometheus.Labels, f func() float64) error {
	gauge := prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{Name: name, Help: help, ConstLabels: labels},
		f,
	)

	if err := m.registry.Register(gauge); err != nil {
		return fmt.Errorf("register gauge %q: %w", name, err)
	}

	return nil
}
