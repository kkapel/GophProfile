package handlers

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// LoggingMiddleware записывает результат каждого HTTP-запроса:
// метод, путь, код ответа, размер тела и длительность обработки.
func LoggingMiddleware(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Обёртка запоминает код ответа и количество записанных байт,
			// которые обычный http.ResponseWriter не отдаёт.
			wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(wrapped, r)

			attrs := []slog.Attr{
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", wrapped.Status()),
				slog.Int("bytes", wrapped.BytesWritten()),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			}

			// Проверяем, есть ли в контексте идентификатор запроса, и если есть,
			// добавляем его в атрибуты.
			if requestID := middleware.GetReqID(r.Context()); requestID != "" {
				attrs = append(attrs, slog.String("request_id", requestID))
			}

			log.LogAttrs(r.Context(), levelByStatus(wrapped.Status()), "http request", attrs...)
		})
	}
}

// levelByStatus выбирает уровень записи по коду ответа:
// серверные ошибки заметнее клиентских.
func levelByStatus(status int) slog.Level {
	switch {
	case status >= http.StatusInternalServerError:
		return slog.LevelError
	case status >= http.StatusBadRequest:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}
