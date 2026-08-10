package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/kkapel/GophProfile/internal/domain"
	"github.com/kkapel/GophProfile/internal/metrics"
	"github.com/kkapel/GophProfile/internal/repository"
	"github.com/kkapel/GophProfile/internal/storage"
)

// newTestWorker создаёт worker с отключённым логированием.
func newTestWorker(repo AvatarRepository, store FileStorage) *Worker {
	return New(repo, store, nil, slog.New(slog.DiscardHandler), metrics.New())
}

// testJPEG возвращает валидное JPEG-изображение заданного размера.
func testJPEG(t *testing.T, width, height int) []byte {
	t.Helper()

	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, height)), nil))

	return buf.Bytes()
}

// uploadEventBody сериализует событие загрузки в тело сообщения.
func uploadEventBody(t *testing.T, avatarID uuid.UUID, s3Key string) []byte {
	t.Helper()

	body, err := json.Marshal(domain.AvatarUploadEvent{
		EventID:  uuid.NewString(),
		AvatarID: avatarID.String(),
		UserID:   "user1",
		S3Key:    s3Key,
	})
	require.NoError(t, err)

	return body
}

// deleteEventBody сериализует событие удаления в тело сообщения.
func deleteEventBody(t *testing.T, keys []string) []byte {
	t.Helper()

	body, err := json.Marshal(domain.AvatarDeleteEvent{
		EventID:  uuid.NewString(),
		AvatarID: uuid.NewString(),
		S3Keys:   keys,
	})
	require.NoError(t, err)

	return body
}

// readCloser — тело объекта хранилища поверх байтов.
type readCloser struct{ io.Reader }

func (readCloser) Close() error { return nil }

func TestHandleUpload_CreatesThumbnails(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := NewMockAvatarRepository(ctrl)
	store := NewMockFileStorage(ctrl)

	id := uuid.New()
	key := "avatars/x/original.jpg"
	data := testJPEG(t, 800, 600)

	repo.EXPECT().IsEventProcessed(gomock.Any(), gomock.Any()).Return(false, nil)

	repo.EXPECT().GetByID(gomock.Any(), id).Return(domain.Avatar{
		ID:               id,
		S3Key:            key,
		ProcessingStatus: domain.ProcessingStatusPending,
	}, nil)

	repo.EXPECT().
		UpdateProcessingStatus(gomock.Any(), id, domain.ProcessingStatusRunning).
		Return(nil)

	store.EXPECT().
		Download(gomock.Any(), key).
		Return(&storage.Object{Body: readCloser{bytes.NewReader(data)}}, nil)

	// Две миниатюры уходят в хранилище.
	uploaded := make([]string, 0, 2)
	store.EXPECT().
		Upload(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), "image/jpeg").
		DoAndReturn(func(_ context.Context, k string, _ any, _ int64, _ string) error {
			uploaded = append(uploaded, k)
			return nil
		}).Times(2)

	repo.EXPECT().
		UpdateProcessingResult(gomock.Any(), id, domain.ProcessingStatusCompleted,
			gomock.Any(), int32(800), int32(600)).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string,
			thumbnails map[string]string, _, _ int32,
		) (domain.Avatar, error) {
			assert.Len(t, thumbnails, 2)
			assert.Contains(t, thumbnails, domain.ThumbSize100)
			assert.Contains(t, thumbnails, domain.ThumbSize300)

			return domain.Avatar{}, nil
		})

	repo.EXPECT().
		MarkEventProcessed(gomock.Any(), gomock.Any(), domain.EventAvatarUploaded).
		Return(nil)

	w := newTestWorker(repo, store)

	require.NoError(t, w.handleUpload(context.Background(), uploadEventBody(t, id, key)))
	assert.Len(t, uploaded, 2)
}

func TestHandleUpload_SkipsAlreadyProcessedEvent(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := NewMockAvatarRepository(ctrl)

	// Событие уже в реестре — дальше проверки работа идти не должна.
	repo.EXPECT().IsEventProcessed(gomock.Any(), gomock.Any()).Return(true, nil)

	w := newTestWorker(repo, NewMockFileStorage(ctrl))

	assert.NoError(t, w.handleUpload(context.Background(), uploadEventBody(t, uuid.New(), "key")))
}

func TestHandleUpload_IdempotentWhenCompleted(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := NewMockAvatarRepository(ctrl)

	id := uuid.New()

	repo.EXPECT().IsEventProcessed(gomock.Any(), gomock.Any()).Return(false, nil)

	// Аватарка уже обработана — дальше GetByID работа идти не должна.
	repo.EXPECT().GetByID(gomock.Any(), id).Return(domain.Avatar{
		ID:               id,
		ProcessingStatus: domain.ProcessingStatusCompleted,
	}, nil)

	w := newTestWorker(repo, NewMockFileStorage(ctrl))

	assert.NoError(t, w.handleUpload(context.Background(), uploadEventBody(t, id, "key")))
}

func TestHandleUpload_AvatarAlreadyDeleted(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := NewMockAvatarRepository(ctrl)

	id := uuid.New()

	repo.EXPECT().IsEventProcessed(gomock.Any(), gomock.Any()).Return(false, nil)
	repo.EXPECT().GetByID(gomock.Any(), id).Return(domain.Avatar{}, repository.ErrNotFound)

	w := newTestWorker(repo, NewMockFileStorage(ctrl))

	// Удалённая аватарка — не ошибка, сообщение подтверждается.
	assert.NoError(t, w.handleUpload(context.Background(), uploadEventBody(t, id, "key")))
}

func TestHandleUpload_MarksFailedOnBrokenImage(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := NewMockAvatarRepository(ctrl)
	store := NewMockFileStorage(ctrl)

	id := uuid.New()
	key := "avatars/x/original.jpg"

	repo.EXPECT().IsEventProcessed(gomock.Any(), gomock.Any()).Return(false, nil)

	repo.EXPECT().GetByID(gomock.Any(), id).Return(domain.Avatar{
		ID:               id,
		S3Key:            key,
		ProcessingStatus: domain.ProcessingStatusPending,
	}, nil)

	repo.EXPECT().UpdateProcessingStatus(gomock.Any(), id, domain.ProcessingStatusRunning).Return(nil)

	// В хранилище лежит не изображение.
	store.EXPECT().Download(gomock.Any(), key).Return(
		&storage.Object{Body: readCloser{bytes.NewReader([]byte("broken"))}}, nil)

	repo.EXPECT().UpdateProcessingStatus(gomock.Any(), id, domain.ProcessingStatusFailed).Return(nil)

	w := newTestWorker(repo, store)

	assert.Error(t, w.handleUpload(context.Background(), uploadEventBody(t, id, key)))
}

func TestHandleUpload_InvalidJSON(t *testing.T) {
	ctrl := gomock.NewController(t)

	w := newTestWorker(NewMockAvatarRepository(ctrl), NewMockFileStorage(ctrl))

	// Битое сообщение подтверждается без повторных попыток.
	assert.NoError(t, w.handleUpload(context.Background(), []byte("{не json")))
}

func TestHandleDelete_RemovesFiles(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := NewMockAvatarRepository(ctrl)
	store := NewMockFileStorage(ctrl)

	keys := []string{
		"avatars/x/original.jpg",
		"thumbnails/x/100x100.jpg",
		"thumbnails/x/300x300.jpg",
	}

	repo.EXPECT().IsEventProcessed(gomock.Any(), gomock.Any()).Return(false, nil)
	store.EXPECT().DeleteMany(gomock.Any(), keys).Return(nil)
	repo.EXPECT().
		MarkEventProcessed(gomock.Any(), gomock.Any(), domain.EventAvatarDeleted).
		Return(nil)

	w := newTestWorker(repo, store)

	assert.NoError(t, w.handleDelete(context.Background(), deleteEventBody(t, keys)))
}

func TestHandleDelete_InvalidJSON(t *testing.T) {
	ctrl := gomock.NewController(t)

	w := newTestWorker(NewMockAvatarRepository(ctrl), NewMockFileStorage(ctrl))

	assert.NoError(t, w.handleDelete(context.Background(), []byte("{не json")))
}
