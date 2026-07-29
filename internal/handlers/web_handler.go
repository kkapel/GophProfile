package handlers

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/kkapel/GophProfile/internal/api"
	"github.com/kkapel/GophProfile/internal/domain"
	"github.com/kkapel/GophProfile/internal/services"
	"github.com/kkapel/GophProfile/web"
)

// WebHandler отдаёт веб-интерфейс: форму загрузки и галерею.
type WebHandler struct {
	service   services.AvatarService
	templates *template.Template
}

// NewWebHandler создаёт обработчик веб-интерфейса и разбирает шаблоны.
func NewWebHandler(service services.AvatarService) (*WebHandler, error) {
	templates, err := template.ParseFS(web.StaticFS, "static/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}

	return &WebHandler{service: service, templates: templates}, nil
}

// galleryItem — одна аватарка в галерее.
type galleryItem struct {
	ID       string
	FileName string
	Status   string
}

// galleryPageData — данные шаблона галереи.
type galleryPageData struct {
	UserID  string
	Avatars []galleryItem
}

// UploadPage обрабатывает GET /web/upload — страницу с формой загрузки.
func (h *WebHandler) UploadPage(w http.ResponseWriter, _ *http.Request) {
	h.render(w, "index.html", nil)
}

// Upload обрабатывает POST /web/upload — приём формы веб-интерфейса.
// Отвечает тем же JSON, что и REST API: его показывает страница загрузки.
func (h *WebHandler) Upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, services.MaxFileSize)

	//nolint:gosec // G120: parsing is bounded by MaxBytesReader
	if err := r.ParseMultipartForm(maxMultipartMemory); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			maxSize := services.MaxFileSize
			writeJSON(w, http.StatusRequestEntityTooLarge, api.Error{
				Error:   "File too large",
				MaxSize: &maxSize,
			})
			return
		}

		writeError(w, http.StatusBadRequest, "Invalid request", "malformed multipart form")
		return
	}
	defer func() {
		_ = r.MultipartForm.RemoveAll()
	}()

	// User ID приходит либо полем формы, либо заголовком.
	userID := r.FormValue("user_id")
	if userID == "" {
		userID = r.Header.Get("X-User-ID")
	}
	if userID == "" {
		writeError(w, http.StatusBadRequest, "Invalid request", "user_id is required")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request", "form field 'file' is required")
		return
	}
	defer func() {
		_ = file.Close()
	}()

	avatar, err := h.service.Upload(r.Context(), services.UploadInput{
		UserID:   userID,
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

// GalleryPage обрабатывает GET /web/gallery/{user_id} — галерею пользователя.
func (h *WebHandler) GalleryPage(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "user_id")

	avatars, err := h.service.List(r.Context(), userID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	items := make([]galleryItem, 0, len(avatars))
	for _, avatar := range avatars {
		items = append(items, galleryItem{
			ID:       avatar.ID.String(),
			FileName: avatar.FileName,
			Status:   normalizeStatus(avatar.ProcessingStatus),
		})
	}

	h.render(w, "gallery.html", galleryPageData{UserID: userID, Avatars: items})
}

// render выполняет шаблон и пишет результат в ответ.
func (h *WebHandler) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if err := h.templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// normalizeStatus приводит внутренний статус обработки к отображаемому.
func normalizeStatus(processingStatus string) string {
	switch processingStatus {
	case domain.ProcessingStatusCompleted:
		return "completed"
	case domain.ProcessingStatusFailed:
		return "failed"
	default:
		return "processing"
	}
}
