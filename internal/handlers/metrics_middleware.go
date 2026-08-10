package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/kkapel/GophProfile/internal/metrics"
)

// MetricsMiddleware собирает RED-метрики по каждому HTTP-запросу
// и уточняет имя спана трассировки.
func MetricsMiddleware(m *metrics.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(wrapped, r)

			// Шаблон маршрута вместо конкретного пути: иначе каждый UUID
			// породил бы отдельную временную серию.
			path := routePattern(r)

			// chi заполняет шаблон маршрута только после обработки запроса,
			// поэтому уточняем имя спана здесь, пока он ещё не закрыт.
			if span := trace.SpanFromContext(r.Context()); span.IsRecording() {
				span.SetName(r.Method + " " + path)
				span.SetAttributes(semconv.HTTPRouteKey.String(path))
			}

			m.HTTPRequestsTotal.WithLabelValues(
				r.Method, path, strconv.Itoa(wrapped.Status()),
			).Inc()

			m.HTTPRequestDuration.WithLabelValues(r.Method, path).
				Observe(time.Since(start).Seconds())
		})
	}
}

// routePattern возвращает шаблон маршрута chi, например
// "/api/v1/avatars/{avatar_id}". Для неизвестных путей — "unknown".
func routePattern(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		if pattern := rctx.RoutePattern(); pattern != "" {
			return pattern
		}
	}

	return "unknown"
}
