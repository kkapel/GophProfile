// Package worker обрабатывает события из брокера сообщений.
package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/kkapel/GophProfile/internal/broker"
	"github.com/kkapel/GophProfile/internal/domain"
	"github.com/kkapel/GophProfile/internal/imageutil"
	"github.com/kkapel/GophProfile/internal/repository"
	"github.com/kkapel/GophProfile/internal/storage"
)

// Параметры повторных попыток обработки.
const (
	maxAttempts  = 3
	baseDelay    = time.Second
	prefetchSize = 4
)

// thumbnailSizes — размеры создаваемых миниатюр.
var thumbnailSizes = map[string][2]int{
	domain.ThumbSize100: {100, 100},
	domain.ThumbSize300: {300, 300},
}

// AvatarRepository — операции над метаданными, необходимые worker'у.
type AvatarRepository interface {
	GetByID(ctx context.Context, id uuid.UUID) (domain.Avatar, error)
	UpdateProcessingStatus(ctx context.Context, id uuid.UUID, status string) error
	UpdateProcessingResult(ctx context.Context, id uuid.UUID, status string,
		thumbnails map[string]string, width, height int32) (domain.Avatar, error)
}

// FileStorage — операции над файлами, необходимые worker'у.
type FileStorage interface {
	Download(ctx context.Context, key string) (*storage.Object, error)
	Upload(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	DeleteMany(ctx context.Context, keys []string) error
}

// Worker обрабатывает события создания миниатюр и удаления файлов.
type Worker struct {
	repo    AvatarRepository
	storage FileStorage
	broker  *broker.RabbitMQ
	log     *slog.Logger
}

// New создаёт worker.
func New(repo AvatarRepository, store FileStorage, b *broker.RabbitMQ, log *slog.Logger) *Worker {
	return &Worker{repo: repo, storage: store, broker: b, log: log}
}

// Run подписывается на очереди и обрабатывает сообщения
// до отмены контекста.
func (w *Worker) Run(ctx context.Context) error {
	uploads, err := w.broker.Consume(broker.QueueProcess, prefetchSize)
	if err != nil {
		return err
	}

	deletes, err := w.broker.Consume(broker.QueueDelete, prefetchSize)
	if err != nil {
		return err
	}

	w.log.Info("worker consuming", "queues", []string{broker.QueueProcess, broker.QueueDelete})

	for {
		select {
		case <-ctx.Done():
			return nil

		case delivery, ok := <-uploads:
			if !ok {
				return errors.New("upload queue channel closed")
			}
			w.process(ctx, delivery, w.handleUpload)

		case delivery, ok := <-deletes:
			if !ok {
				return errors.New("delete queue channel closed")
			}
			w.process(ctx, delivery, w.handleDelete)
		}
	}
}

// process выполняет обработчик с повторными попытками и подтверждает
// сообщение брокеру.
func (w *Worker) process(ctx context.Context, delivery amqp.Delivery, handler func(context.Context, []byte) error) {
	var err error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err = handler(ctx, delivery.Body); err == nil {
			// Подтверждаем обработку: сообщение удаляется из очереди.
			if ackErr := delivery.Ack(false); ackErr != nil {
				w.log.Error("ack message", "err", ackErr)
			}
			return
		}

		w.log.Warn("handle message failed", "attempt", attempt, "err", err)

		if attempt < maxAttempts {
			// Экспоненциальная задержка: 1с, 2с, 4с...
			delay := baseDelay * time.Duration(1<<(attempt-1))
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}
	}

	w.log.Error("message dropped after retries", "err", err)

	// requeue=false: сообщение не возвращается в очередь, иначе
	// оно будет обрабатываться бесконечно.
	if nackErr := delivery.Nack(false, false); nackErr != nil {
		w.log.Error("nack message", "err", nackErr)
	}
}

// handleUpload создаёт миниатюры для загруженной аватарки.
func (w *Worker) handleUpload(ctx context.Context, body []byte) error {
	var event domain.AvatarUploadEvent
	if err := json.Unmarshal(body, &event); err != nil {
		w.log.Error("skip malformed upload event", "err", err)
		return nil
	}

	avatarID, err := uuid.Parse(event.AvatarID)
	if err != nil {
		w.log.Error("skip event with invalid avatar id", "avatar_id", event.AvatarID, "err", err)
		return nil
	}

	avatar, err := w.repo.GetByID(ctx, avatarID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// Аватарку уже удалили — обрабатывать нечего.
			w.log.Info("avatar not found, skipping", "avatar_id", event.AvatarID)
			return nil
		}
		return err
	}

	// Идемпотентность: повторная доставка того же события
	// не должна выполнять работу заново.
	if avatar.ProcessingStatus == domain.ProcessingStatusCompleted {
		w.log.Info("avatar already processed, skipping", "avatar_id", event.AvatarID)
		return nil
	}

	if err := w.repo.UpdateProcessingStatus(ctx, avatarID, domain.ProcessingStatusRunning); err != nil {
		return err
	}

	thumbnails, width, height, err := w.makeThumbnails(ctx, event.AvatarID, event.S3Key)
	if err != nil {
		// Помечаем неудачу, чтобы статус не остался в processing навсегда.
		_ = w.repo.UpdateProcessingStatus(ctx, avatarID, domain.ProcessingStatusFailed)
		return err
	}

	if _, err := w.repo.UpdateProcessingResult(
		ctx, avatarID, domain.ProcessingStatusCompleted, thumbnails, width, height,
	); err != nil {
		return err
	}

	w.log.Info("avatar processed", "avatar_id", event.AvatarID, "thumbnails", len(thumbnails))

	return nil
}

// makeThumbnails скачивает оригинал, создаёт миниатюры и загружает их
// в хранилище. Возвращает карту размер -> ключ и размеры оригинала.
func (w *Worker) makeThumbnails(ctx context.Context, avatarID, s3Key string) (
	thumbnails map[string]string, width, height int32, err error,
) {
	obj, err := w.storage.Download(ctx, s3Key)
	if err != nil {
		return nil, 0, 0, err
	}
	defer func() {
		_ = obj.Body.Close()
	}()

	img, err := imageutil.Decode(obj.Body)
	if err != nil {
		return nil, 0, 0, err
	}

	width, height = imageutil.Dimensions(img)
	thumbnails = make(map[string]string, len(thumbnailSizes))

	for size, dims := range thumbnailSizes {
		data, err := imageutil.Thumbnail(img, dims[0], dims[1])
		if err != nil {
			return nil, 0, 0, err
		}

		key := storage.ThumbnailKey(avatarID, size)
		if err := w.storage.Upload(ctx, key, bytes.NewReader(data), int64(len(data)), "image/jpeg"); err != nil {
			return nil, 0, 0, err
		}

		thumbnails[size] = key
	}

	return thumbnails, width, height, nil
}

// handleDelete удаляет файлы аватарки из хранилища.
func (w *Worker) handleDelete(ctx context.Context, body []byte) error {
	var event domain.AvatarDeleteEvent
	if err := json.Unmarshal(body, &event); err != nil {
		w.log.Error("skip malformed delete event", "err", err)
		return nil
	}

	// Удаление в S3 идемпотентно: повторный вызов не вернёт ошибку.
	if err := w.storage.DeleteMany(ctx, event.S3Keys); err != nil {
		return err
	}

	w.log.Info("avatar files deleted", "avatar_id", event.AvatarID, "keys", len(event.S3Keys))

	return nil
}
