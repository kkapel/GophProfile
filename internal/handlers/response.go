// Package handlers содержит HTTP-обработчики REST API.
package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/kkapel/GophProfile/internal/api"
)

// writeJSON отправляет ответ в формате JSON с указанным кодом статуса.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if payload == nil {
		return
	}

	// Ошибку кодирования логировать некуда: заголовки уже отправлены.
	_ = json.NewEncoder(w).Encode(payload)
}

// writeError отправляет ошибку в формате, описанном в OpenAPI-спецификации.
func writeError(w http.ResponseWriter, status int, message, details string) {
	payload := api.Error{Error: message}
	if details != "" {
		payload.Details = &details
	}

	writeJSON(w, status, payload)
}
