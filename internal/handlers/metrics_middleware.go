package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/kkapel/GophProfile/internal/metrics"
)

// MetricsMiddleware собирает RED-метрики по каждому HTTP-запросу.
func MetricsMiddleware(m *metrics.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(wrapped, r)

			// Шаблон маршрута вместо конкретного пути: иначе каждый UUID
			// породил бы отдельную временную серию.
			path := routePattern(r)

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
