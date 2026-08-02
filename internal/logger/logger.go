// Package logger создаёт логгеры приложения
// и настраивает экспорт записей в OpenTelemetry Collector.
package logger

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
	"go.opentelemetry.io/otel/trace"
)

// ShutdownFunc завершает работу gRPC Exporter'а, дожидаясь отправки буфера.
type ShutdownFunc func(ctx context.Context) error

// New создаёт логгер, который пишет одновременно в stdout и отправляет записи
// в OpenTelemetry Collector через gRPC Exporter.
// Адрес коллектора берётся из переменных окружения OTEL_*.
// Возвращённую функцию завершения нужно вызвать перед выходом из программы.
// Допустимые уровни: DEBUG, INFO, WARN, ERROR.
func New(ctx context.Context, serviceName, serviceVersion, level string) (*slog.Logger, ShutdownFunc, error) {
	parsed, err := parseLevel(level)
	if err != nil {
		return nil, nil, err
	}

	// gRPC Exporter читает адрес коллектора из OTEL_EXPORTER_OTLP_ENDPOINT.
	exporter, err := otlploggrpc.New(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("create otlp log exporter: %w", err)
	}

	// Resource описывает, кто отправил запись. Эти атрибуты станут
	// метками потока в Loki.
	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
			semconv.ServiceVersionKey.String(serviceVersion),
		),
	)

	if err != nil {
		return nil, nil, fmt.Errorf("create resource: %w", err)
	}

	// BatchProcessor копит записи и передаёт их gRPC Exporter'у пакетами.
	provider := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
	)

	handler := &fanoutHandler{
		minLevel: parsed,
		handlers: []slog.Handler{
			// Локальный вывод: docker compose logs продолжает работать.
			slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parsed}),
			// Мост между slog и OpenTelemetry.
			otelslog.NewHandler(serviceName, otelslog.WithLoggerProvider(provider)),
		},
	}

	return slog.New(handler), provider.Shutdown, nil
}

// fanoutHandler дублирует записи в несколько обработчиков
// и добавляет идентификаторы трассировки из контекста.
type fanoutHandler struct {
	minLevel slog.Level
	handlers []slog.Handler
}

// Enabled сообщает, нужно ли обрабатывать запись указанного уровня.
func (h *fanoutHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.minLevel
}

// Handle передаёт запись всем вложенным обработчикам.
func (h *fanoutHandler) Handle(ctx context.Context, record slog.Record) error {
	// Корреляция логов и трассировок: идентификаторы попадают в stdout,
	// в OTLP они уезжают отдельным полем самого протокола.
	if spanCtx := trace.SpanContextFromContext(ctx); spanCtx.IsValid() {
		record.AddAttrs(
			slog.String("trace_id", spanCtx.TraceID().String()),
			slog.String("span_id", spanCtx.SpanID().String()),
		)
	}

	var errs []error
	for _, handler := range h.handlers {
		if err := handler.Handle(ctx, record.Clone()); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// WithAttrs возвращает обработчик с постоянными атрибутами.
func (h *fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		next = append(next, handler.WithAttrs(attrs))
	}

	return &fanoutHandler{minLevel: h.minLevel, handlers: next}
}

// WithGroup возвращает обработчик с вложенной группой атрибутов.
func (h *fanoutHandler) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		next = append(next, handler.WithGroup(name))
	}

	return &fanoutHandler{minLevel: h.minLevel, handlers: next}
}

// parseLevel переводит строковый уровень логирования в slog.Level.
func parseLevel(level string) (slog.Level, error) {
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "DEBUG":
		return slog.LevelDebug, nil
	case "INFO":
		return slog.LevelInfo, nil
	case "WARN":
		return slog.LevelWarn, nil
	case "ERROR":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown logger level %q", level)
	}
}
