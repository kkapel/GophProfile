package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kkapel/GophProfile/internal/handlers"
)

// stubPinger — компонент с заранее заданным результатом проверки.
type stubPinger struct{ err error }

func (s stubPinger) Ping(_ context.Context) error { return s.err }

func TestHealth_AllComponentsUp(t *testing.T) {
	handler := handlers.NewHealthHandler(map[string]handlers.Pinger{
		"database": stubPinger{},
		"storage":  stubPinger{},
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Status     string            `json:"status"`
		Components map[string]string `json:"components"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	assert.Equal(t, "ok", resp.Status)
	assert.Equal(t, "ok", resp.Components["database"])
}

func TestHealth_ComponentDown(t *testing.T) {
	handler := handlers.NewHealthHandler(map[string]handlers.Pinger{
		"database": stubPinger{},
		"storage":  stubPinger{err: errors.New("connection refused")},
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var resp struct {
		Status     string            `json:"status"`
		Components map[string]string `json:"components"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	assert.Equal(t, "degraded", resp.Status)
	assert.Equal(t, "ok", resp.Components["database"])
	assert.Contains(t, resp.Components["storage"], "down")
}
