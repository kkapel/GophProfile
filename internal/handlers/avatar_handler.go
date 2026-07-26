package handlers

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/kkapel/GophProfile/internal/api"
	"github.com/kkapel/GophProfile/internal/services"
)

// maxMultipartMemory — сколько данных формы держать в памяти,
// остальное Go выгружает во временные файлы на диске.
const maxMultipartMemory = 1 << 20 // 1 MB

// AvatarHandler реализует api.ServerInterface — REST API сервиса аватарок.
type AvatarHandler struct {
	service services.AvatarService
}

// NewAvatarHandler создаёт обработчик REST API.
func NewAvatarHandler(service services.AvatarService) *AvatarHandler {
	return &AvatarHandler{service: service}
}

// UploadAvatar обрабатывает POST /api/v1/avatars.
func (h *AvatarHandler) UploadAvatar(w http.ResponseWriter, r *http.Request, params api.UploadAvatarParams) {
	// MaxBytesReader обрывает чтение, если тело превышает лимит,
	// поэтому огромный файл не окажется целиком в памяти.
	r.Body = http.MaxBytesReader(w, r.Body, services.MaxFileSize)

	// Размер тела уже ограничен MaxBytesReader выше, в памяти держим не более 1 MB.
	//nolint:gosec // G120: parsing is bounded by MaxBytesReader
	if err := r.ParseMultipartForm(maxMultipartMemory); err != nil {
		maxSize := services.MaxFileSize
		writeJSON(w, http.StatusRequestEntityTooLarge, api.Error{
			Error:   "File too large",
			MaxSize: &maxSize,
		})
		return
	}
	defer func() {
		_ = r.MultipartForm.RemoveAll()
	}()

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request", "form field 'file' is required")
		return
	}
	defer func() {
		_ = file.Close()
	}()

	avatar, err := h.service.Upload(r.Context(), services.UploadInput{
		UserID:   params.XUserID,
		FileName: header.Filename,
		Size:     header.Size,
		File:     file,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, toAPIAvatar(avatar))
}

// GetAvatar обрабатывает GET /api/v1/avatars/{avatar_id}.
func (h *AvatarHandler) GetAvatar(w http.ResponseWriter, r *http.Request, avatarID api.AvatarID, params api.GetAvatarParams) {
	size := ""
	if params.Size != nil {
		size = string(*params.Size)
	}

	obj, err := h.service.GetFile(r.Context(), avatarID, size)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	defer func() {
		_ = obj.Body.Close()
	}()

	writeImage(w, obj.ContentType, obj.Size, obj.Body)
}

// GetUserAvatar обрабатывает GET /api/v1/users/{user_id}/avatar.
func (h *AvatarHandler) GetUserAvatar(w http.ResponseWriter, r *http.Request, userID api.UserID, params api.GetUserAvatarParams) {
	size := ""
	if params.Size != nil {
		size = string(*params.Size)
	}

	obj, err := h.service.GetUserFile(r.Context(), userID, size)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	defer func() {
		_ = obj.Body.Close()
	}()

	writeImage(w, obj.ContentType, obj.Size, obj.Body)
}

// GetAvatarMetadata обрабатывает GET /api/v1/avatars/{avatar_id}/metadata.
func (h *AvatarHandler) GetAvatarMetadata(w http.ResponseWriter, r *http.Request, avatarID api.AvatarID) {
	avatar, err := h.service.GetMetadata(r.Context(), avatarID)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, toAPIMetadata(avatar))
}

// ListUserAvatars обрабатывает GET /api/v1/users/{user_id}/avatars.
func (h *AvatarHandler) ListUserAvatars(w http.ResponseWriter, r *http.Request, userID api.UserID) {
	avatars, err := h.service.List(r.Context(), userID)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	result := make([]api.Avatar, 0, len(avatars))
	for _, avatar := range avatars {
		result = append(result, toAPIAvatar(avatar))
	}

	writeJSON(w, http.StatusOK, result)
}

// DeleteAvatar обрабатывает DELETE /api/v1/avatars/{avatar_id}.
func (h *AvatarHandler) DeleteAvatar(w http.ResponseWriter, r *http.Request, avatarID api.AvatarID, params api.DeleteAvatarParams) {
	if err := h.service.Delete(r.Context(), avatarID, params.XUserID); err != nil {
		writeServiceError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DeleteUserAvatar обрабатывает DELETE /api/v1/users/{user_id}/avatar.
func (h *AvatarHandler) DeleteUserAvatar(w http.ResponseWriter, r *http.Request, userID api.UserID, params api.DeleteUserAvatarParams) {
	if err := h.service.DeleteUserAvatar(r.Context(), userID, params.XUserID); err != nil {
		writeServiceError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// writeImage отправляет бинарные данные изображения.
func writeImage(w http.ResponseWriter, contentType string, size int64, body io.Reader) {
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	w.Header().Set("Cache-Control", "max-age=86400")
	w.WriteHeader(http.StatusOK)

	// Ошибку копирования обработать нельзя: статус уже отправлен клиенту.
	_, _ = io.Copy(w, body)
}

// writeServiceError переводит ошибки сервисного слоя в HTTP-ответы.
func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, services.ErrNotFound):
		writeError(w, http.StatusNotFound, "Avatar not found", "")
	case errors.Is(err, services.ErrForbidden):
		writeError(w, http.StatusForbidden, "Forbidden", "You can only delete your own avatars")
	case errors.Is(err, services.ErrUnsupportedFormat):
		writeError(w, http.StatusBadRequest, "Invalid file format", "Supported formats: jpeg, png, webp")
	case errors.Is(err, services.ErrFileTooLarge):
		maxSize := services.MaxFileSize
		writeJSON(w, http.StatusRequestEntityTooLarge, api.Error{
			Error:   "File too large",
			MaxSize: &maxSize,
		})
	default:
		writeError(w, http.StatusInternalServerError, "Internal server error", "")
	}
}
