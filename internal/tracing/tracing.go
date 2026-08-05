// Package tracing настраивает распределённую трассировку OpenTelemetry.
package tracing

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
)

// ShutdownFunc завершает работу gRPC Exporter'а, дожидаясь отправки буфера.
type ShutdownFunc func(ctx context.Context) error

// Init настраивает провайдер трассировки и глобальный пропагатор контекста.
// Адрес коллектора берётся из переменных окружения OTEL_*.
// Возвращённую функцию завершения нужно вызвать перед выходом из программы.
func Init(ctx context.Context, serviceName, serviceVersion string) (ShutdownFunc, error) {
	// gRPC Exporter читает адрес коллектора из OTEL_EXPORTER_OTLP_ENDPOINT.
	exporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create otlp trace exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
			semconv.ServiceVersionKey.String(serviceVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create resource: %w", err)
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		// BatchSpanProcessor копит спаны и передаёт их gRPC Exporter'у пакетами.
		sdktrace.WithBatcher(exporter),
		// AlwaysSample записывает 100% запросов
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	otel.SetTracerProvider(provider)

	// Propagator переносит trace-контекст между процессами через заголовки.
	// Без него трейс обрывается на границе сервисов.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return provider.Shutdown, nil
}
