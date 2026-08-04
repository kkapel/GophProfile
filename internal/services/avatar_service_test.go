package services_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/kkapel/GophProfile/internal/domain"
	"github.com/kkapel/GophProfile/internal/metrics"
	"github.com/kkapel/GophProfile/internal/mocks"
	"github.com/kkapel/GophProfile/internal/repository"
	"github.com/kkapel/GophProfile/internal/services"
	"github.com/kkapel/GophProfile/internal/storage"
)

// testJPEG возвращает валидное JPEG-изображение для проверки MIME-типа.
func testJPEG(t *testing.T) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 20, 10))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})

	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))

	return buf.Bytes()
}

// nopCloser — пустое тело объекта хранилища.
type nopCloser struct{}

func (nopCloser) Read([]byte) (int, error) { return 0, nil }
func (nopCloser) Close() error             { return nil }

func TestUpload_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockAvatarRepository(ctrl)
	store := mocks.NewMockFileStorage(ctrl)
	publisher := mocks.NewMockEventPublisher(ctrl)

	data := testJPEG(t)

	var uploadedKey string
	store.EXPECT().
		Upload(gomock.Any(), gomock.Any(), gomock.Any(), int64(len(data)), "image/jpeg").
		DoAndReturn(func(_ context.Context, key string, _ any, _ int64, _ string) error {
			uploadedKey = key
			return nil
		})

	repo.EXPECT().
		Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, avatar domain.Avatar) (domain.Avatar, error) {
			return avatar, nil
		})

	publisher.EXPECT().
		Publish(gomock.Any(), domain.EventAvatarUploaded, gomock.Any()).
		Return(nil)

	service := services.NewAvatarService(repo, store, publisher, slog.New(slog.DiscardHandler), metrics.New())

	avatar, err := service.Upload(context.Background(), services.UploadInput{
		UserID:   "user1",
		FileName: "photo.jpg",
		Size:     int64(len(data)),
		File:     bytes.NewReader(data),
	})

	require.NoError(t, err)
	assert.Equal(t, "user1", avatar.UserID)
	assert.Equal(t, "image/jpeg", avatar.MimeType)
	assert.Equal(t, domain.ProcessingStatusPending, avatar.ProcessingStatus)
	assert.Contains(t, uploadedKey, "original.jpg")
}

func TestUpload_FileTooLarge(t *testing.T) {
	ctrl := gomock.NewController(t)

	// Ни один метод не должен быть вызван — gomock проверит это сам.
	service := services.NewAvatarService(
		mocks.NewMockAvatarRepository(ctrl),
		mocks.NewMockFileStorage(ctrl),
		mocks.NewMockEventPublisher(ctrl),
		slog.New(slog.DiscardHandler),
		metrics.New(),
	)

	_, err := service.Upload(context.Background(), services.UploadInput{
		UserID: "user1",
		Size:   services.MaxFileSize + 1,
		File:   bytes.NewReader(nil),
	})

	assert.ErrorIs(t, err, services.ErrFileTooLarge)
}

func TestUpload_UnsupportedFormat(t *testing.T) {
	ctrl := gomock.NewController(t)

	service := services.NewAvatarService(
		mocks.NewMockAvatarRepository(ctrl),
		mocks.NewMockFileStorage(ctrl),
		mocks.NewMockEventPublisher(ctrl),
		slog.New(slog.DiscardHandler),
		metrics.New(),
	)

	data := []byte("это обычный текст, а не изображение")

	_, err := service.Upload(context.Background(), services.UploadInput{
		UserID: "user1",
		Size:   int64(len(data)),
		File:   bytes.NewReader(data),
	})

	assert.ErrorIs(t, err, services.ErrUnsupportedFormat)
}

func TestUpload_RepoErrorRemovesFile(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockAvatarRepository(ctrl)
	store := mocks.NewMockFileStorage(ctrl)
	publisher := mocks.NewMockEventPublisher(ctrl)

	data := testJPEG(t)
	repoErr := errors.New("db is down")

	var uploadedKey string
	store.EXPECT().
		Upload(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, key string, _ any, _ int64, _ string) error {
			uploadedKey = key
			return nil
		})

	repo.EXPECT().
		Create(gomock.Any(), gomock.Any()).
		Return(domain.Avatar{}, repoErr)

	// Компенсация: загруженный файл должен быть удалён.
	store.EXPECT().
		Delete(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, key string) error {
			assert.Equal(t, uploadedKey, key)
			return nil
		})

	service := services.NewAvatarService(repo, store, publisher, slog.New(slog.DiscardHandler), metrics.New())

	_, err := service.Upload(context.Background(), services.UploadInput{
		UserID: "user1",
		Size:   int64(len(data)),
		File:   bytes.NewReader(data),
	})

	assert.ErrorIs(t, err, repoErr)
}

func TestDelete_Forbidden(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockAvatarRepository(ctrl)

	id := uuid.New()
	repo.EXPECT().
		GetByID(gomock.Any(), id).
		Return(domain.Avatar{ID: id, UserID: "owner"}, nil)

	service := services.NewAvatarService(repo, mocks.NewMockFileStorage(ctrl), mocks.NewMockEventPublisher(ctrl), slog.New(slog.DiscardHandler), metrics.New())

	err := service.Delete(context.Background(), id, "stranger")

	assert.ErrorIs(t, err, services.ErrForbidden)
}

func TestDelete_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockAvatarRepository(ctrl)
	publisher := mocks.NewMockEventPublisher(ctrl)

	id := uuid.New()
	avatar := domain.Avatar{
		ID:              id,
		UserID:          "user1",
		S3Key:           "avatars/x/original.jpg",
		ThumbnailS3Keys: map[string]string{domain.ThumbSize100: "thumbnails/x/100x100.jpg"},
	}

	repo.EXPECT().GetByID(gomock.Any(), id).Return(avatar, nil)
	repo.EXPECT().SoftDelete(gomock.Any(), id).Return(avatar, nil)

	var published domain.AvatarDeleteEvent
	publisher.EXPECT().
		Publish(gomock.Any(), domain.EventAvatarDeleted, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, event any) error {
			published, _ = event.(domain.AvatarDeleteEvent)
			return nil
		})

	service := services.NewAvatarService(repo, mocks.NewMockFileStorage(ctrl), publisher, slog.New(slog.DiscardHandler), metrics.New())

	require.NoError(t, service.Delete(context.Background(), id, "user1"))

	// В событие попадают и оригинал, и миниатюры.
	assert.Len(t, published.S3Keys, 2)
	assert.Equal(t, id.String(), published.AvatarID)
}

func TestDelete_NotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockAvatarRepository(ctrl)

	repo.EXPECT().
		GetByID(gomock.Any(), gomock.Any()).
		Return(domain.Avatar{}, repository.ErrNotFound)

	service := services.NewAvatarService(repo, mocks.NewMockFileStorage(ctrl), mocks.NewMockEventPublisher(ctrl), slog.New(slog.DiscardHandler), metrics.New())

	err := service.Delete(context.Background(), uuid.New(), "user1")

	assert.ErrorIs(t, err, services.ErrNotFound)
}

func TestGetFile_UsesThumbnailKey(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockAvatarRepository(ctrl)
	store := mocks.NewMockFileStorage(ctrl)

	id := uuid.New()
	repo.EXPECT().GetByID(gomock.Any(), id).Return(domain.Avatar{
		ID:              id,
		S3Key:           "avatars/x/original.jpg",
		ThumbnailS3Keys: map[string]string{domain.ThumbSize100: "thumbnails/x/100x100.jpg"},
	}, nil)

	store.EXPECT().
		Download(gomock.Any(), "thumbnails/x/100x100.jpg").
		Return(&storage.Object{Body: nopCloser{}}, nil)

	service := services.NewAvatarService(repo, store, mocks.NewMockEventPublisher(ctrl), slog.New(slog.DiscardHandler), metrics.New())

	obj, err := service.GetFile(context.Background(), id, domain.ThumbSize100)
	require.NoError(t, err)
	require.NoError(t, obj.Body.Close())
}

func TestGetFile_FallbackToOriginal(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockAvatarRepository(ctrl)
	store := mocks.NewMockFileStorage(ctrl)

	id := uuid.New()
	// Миниатюры ещё не готовы.
	repo.EXPECT().GetByID(gomock.Any(), id).Return(domain.Avatar{
		ID:    id,
		S3Key: "avatars/x/original.jpg",
	}, nil)

	store.EXPECT().
		Download(gomock.Any(), "avatars/x/original.jpg").
		Return(&storage.Object{Body: nopCloser{}}, nil)

	service := services.NewAvatarService(repo, store, mocks.NewMockEventPublisher(ctrl), slog.New(slog.DiscardHandler), metrics.New())

	obj, err := service.GetFile(context.Background(), id, domain.ThumbSize300)
	require.NoError(t, err)
	require.NoError(t, obj.Body.Close())
}

func TestList_ReturnsAvatars(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockAvatarRepository(ctrl)

	repo.EXPECT().
		ListByUserID(gomock.Any(), "user1").
		Return([]domain.Avatar{{UserID: "user1"}, {UserID: "user1"}}, nil)

	service := services.NewAvatarService(repo, mocks.NewMockFileStorage(ctrl), mocks.NewMockEventPublisher(ctrl), slog.New(slog.DiscardHandler), metrics.New())

	avatars, err := service.List(context.Background(), "user1")

	require.NoError(t, err)
	assert.Len(t, avatars, 2)
}

func TestGetMetadata_NotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockAvatarRepository(ctrl)

	repo.EXPECT().
		GetByID(gomock.Any(), gomock.Any()).
		Return(domain.Avatar{}, repository.ErrNotFound)

	service := services.NewAvatarService(repo, mocks.NewMockFileStorage(ctrl), mocks.NewMockEventPublisher(ctrl), slog.New(slog.DiscardHandler), metrics.New())

	_, err := service.GetMetadata(context.Background(), uuid.New())

	assert.ErrorIs(t, err, services.ErrNotFound)
}
