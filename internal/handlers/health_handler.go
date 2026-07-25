package handlers

import (
	"context"
	"net/http"
	"time"
)

// Pinger описывает компонент, доступность которого можно проверить.
type Pinger interface {
	Ping(ctx context.Context) error
}

// HealthHandler проверяет доступность зависимостей сервиса.
type HealthHandler struct {
	components map[string]Pinger
}

// NewHealthHandler создаёт обработчик проверки работоспособности.
// Ключи карты попадают в ответ как имена компонентов.
func NewHealthHandler(components map[string]Pinger) *HealthHandler {
	return &HealthHandler{components: components}
}

// healthResponse — тело ответа проверки работоспособности.
type healthResponse struct {
	Status     string            `json:"status"`
	Components map[string]string `json:"components"`
}

// ServeHTTP обрабатывает GET /health.
func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	resp := healthResponse{
		Status:     "ok",
		Components: make(map[string]string, len(h.components)),
	}
	status := http.StatusOK

	for name, component := range h.components {
		if err := component.Ping(ctx); err != nil {
			resp.Components[name] = "down: " + err.Error()
			resp.Status = "degraded"
			status = http.StatusServiceUnavailable
			continue
		}
		resp.Components[name] = "ok"
	}

	writeJSON(w, status, resp)
}
