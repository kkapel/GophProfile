package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/kkapel/GophProfile/internal/api"
	"github.com/kkapel/GophProfile/internal/domain"
	"github.com/kkapel/GophProfile/internal/handlers"
	"github.com/kkapel/GophProfile/internal/mocks"
	"github.com/kkapel/GophProfile/internal/services"
	"github.com/kkapel/GophProfile/internal/storage"
)

// newRouter собирает роутер с REST API поверх переданного сервиса.
func newRouter(service services.AvatarService) http.Handler {
	r := chi.NewRouter()
	api.HandlerFromMuxWithBaseURL(handlers.NewAvatarHandler(service, slog.New(slog.DiscardHandler)), r, "/api/v1")

	return r
}

// multipartBody формирует тело multipart-запроса с файлом.
func multipartBody(t *testing.T, fieldName, fileName string, content []byte) (io.Reader, string) {
	t.Helper()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	part, err := writer.CreateFormFile(fieldName, fileName)
	require.NoError(t, err)

	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	return &buf, writer.FormDataContentType()
}

// nopCloser — пустое тело объекта хранилища.
type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

func TestUploadAvatar_Created(t *testing.T) {
	ctrl := gomock.NewController(t)
	service := mocks.NewMockAvatarService(ctrl)

	id := uuid.New()
	created := time.Now().UTC()

	service.EXPECT().
		Upload(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, in services.UploadInput) (domain.Avatar, error) {
			assert.Equal(t, "user1", in.UserID)
			assert.Equal(t, "photo.jpg", in.FileName)

			return domain.Avatar{
				ID:               id,
				UserID:           in.UserID,
				ProcessingStatus: domain.ProcessingStatusPending,
				CreatedAt:        created,
			}, nil
		})

	body, contentType := multipartBody(t, "file", "photo.jpg", []byte("fake-image-bytes"))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/avatars", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-User-ID", "user1")

	rec := httptest.NewRecorder()
	newRouter(service).ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)

	var resp api.Avatar
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	assert.Equal(t, id, resp.Id)
	assert.Equal(t, "user1", resp.UserId)
	assert.Equal(t, api.Processing, resp.Status)
	assert.Contains(t, resp.Url, id.String())
}

func TestUploadAvatar_MissingUserHeader(t *testing.T) {
	ctrl := gomock.NewController(t)
	// Сервис не должен быть вызван: запрос отсечён на уровне разбора параметров.
	service := mocks.NewMockAvatarService(ctrl)

	body, contentType := multipartBody(t, "file", "photo.jpg", []byte("data"))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/avatars", body)
	req.Header.Set("Content-Type", contentType)

	rec := httptest.NewRecorder()
	newRouter(service).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUploadAvatar_WrongFormField(t *testing.T) {
	ctrl := gomock.NewController(t)
	service := mocks.NewMockAvatarService(ctrl)

	// Поле называется image вместо file.
	body, contentType := multipartBody(t, "image", "photo.jpg", []byte("data"))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/avatars", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-User-ID", "user1")

	rec := httptest.NewRecorder()
	newRouter(service).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUploadAvatar_UnsupportedFormat(t *testing.T) {
	ctrl := gomock.NewController(t)
	service := mocks.NewMockAvatarService(ctrl)

	service.EXPECT().
		Upload(gomock.Any(), gomock.Any()).
		Return(domain.Avatar{}, services.ErrUnsupportedFormat)

	body, contentType := multipartBody(t, "file", "doc.txt", []byte("plain text"))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/avatars", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-User-ID", "user1")

	rec := httptest.NewRecorder()
	newRouter(service).ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)

	var resp api.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "Invalid file format", resp.Error)
}

func TestGetAvatar_ReturnsBinary(t *testing.T) {
	ctrl := gomock.NewController(t)
	service := mocks.NewMockAvatarService(ctrl)

	id := uuid.New()
	content := []byte("binary-image-content")

	service.EXPECT().
		GetFile(gomock.Any(), id, "100x100").
		Return(&storage.Object{
			Body:        nopCloser{bytes.NewReader(content)},
			ContentType: "image/jpeg",
			Size:        int64(len(content)),
		}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+id.String()+"?size=100x100", nil)
	rec := httptest.NewRecorder()
	newRouter(service).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"))
	assert.Equal(t, "max-age=86400", rec.Header().Get("Cache-Control"))
	assert.Equal(t, content, rec.Body.Bytes())
}

func TestGetAvatar_NotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	service := mocks.NewMockAvatarService(ctrl)

	id := uuid.New()
	service.EXPECT().
		GetFile(gomock.Any(), id, gomock.Any()).
		Return(nil, services.ErrNotFound)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+id.String(), nil)
	rec := httptest.NewRecorder()
	newRouter(service).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestGetAvatar_InvalidUUID(t *testing.T) {
	ctrl := gomock.NewController(t)
	// Некорректный UUID отсекается сгенерированным слоем разбора параметров.
	service := mocks.NewMockAvatarService(ctrl)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/not-a-uuid", nil)
	rec := httptest.NewRecorder()
	newRouter(service).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestGetAvatarMetadata_Full(t *testing.T) {
	ctrl := gomock.NewController(t)
	service := mocks.NewMockAvatarService(ctrl)

	id := uuid.New()
	width, height := int32(1920), int32(1080)

	service.EXPECT().
		GetMetadata(gomock.Any(), id).
		Return(domain.Avatar{
			ID:        id,
			UserID:    "user1",
			FileName:  "photo.jpg",
			MimeType:  "image/jpeg",
			SizeBytes: 1024,
			Width:     &width,
			Height:    &height,
			ThumbnailS3Keys: map[string]string{
				domain.ThumbSize100: "thumbnails/x/100x100.jpg",
				domain.ThumbSize300: "thumbnails/x/300x300.jpg",
			},
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+id.String()+"/metadata", nil)
	rec := httptest.NewRecorder()
	newRouter(service).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp api.AvatarMetadata
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	assert.Equal(t, "photo.jpg", resp.FileName)
	assert.Equal(t, int64(1024), resp.Size)

	require.NotNil(t, resp.Dimensions)
	assert.Equal(t, 1920, resp.Dimensions.Width)

	require.NotNil(t, resp.Thumbnails)
	assert.Len(t, *resp.Thumbnails, 2)
}

func TestListUserAvatars(t *testing.T) {
	ctrl := gomock.NewController(t)
	service := mocks.NewMockAvatarService(ctrl)

	service.EXPECT().
		List(gomock.Any(), "user1").
		Return([]domain.Avatar{
			{ID: uuid.New(), UserID: "user1", ProcessingStatus: domain.ProcessingStatusCompleted},
			{ID: uuid.New(), UserID: "user1", ProcessingStatus: domain.ProcessingStatusPending},
		}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/user1/avatars", nil)
	rec := httptest.NewRecorder()
	newRouter(service).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp []api.Avatar
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	require.Len(t, resp, 2)
	assert.Equal(t, api.Completed, resp[0].Status)
	assert.Equal(t, api.Processing, resp[1].Status)
}

func TestDeleteAvatar_NoContent(t *testing.T) {
	ctrl := gomock.NewController(t)
	service := mocks.NewMockAvatarService(ctrl)

	id := uuid.New()
	service.EXPECT().Delete(gomock.Any(), id, "user1").Return(nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/avatars/"+id.String(), nil)
	req.Header.Set("X-User-ID", "user1")

	rec := httptest.NewRecorder()
	newRouter(service).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, rec.Body.Bytes())
}

func TestDeleteAvatar_Forbidden(t *testing.T) {
	ctrl := gomock.NewController(t)
	service := mocks.NewMockAvatarService(ctrl)

	id := uuid.New()
	service.EXPECT().
		Delete(gomock.Any(), id, "stranger").
		Return(services.ErrForbidden)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/avatars/"+id.String(), nil)
	req.Header.Set("X-User-ID", "stranger")

	rec := httptest.NewRecorder()
	newRouter(service).ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)

	var resp api.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "Forbidden", resp.Error)
}

func TestDeleteUserAvatar_NoContent(t *testing.T) {
	ctrl := gomock.NewController(t)
	service := mocks.NewMockAvatarService(ctrl)

	service.EXPECT().DeleteUserAvatar(gomock.Any(), "user1", "user1").Return(nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/users/user1/avatar", nil)
	req.Header.Set("X-User-ID", "user1")

	rec := httptest.NewRecorder()
	newRouter(service).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
}
