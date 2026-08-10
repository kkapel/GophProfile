// Package services содержит бизнес-логику сервиса аватарок.
package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/kkapel/GophProfile/internal/domain"
	"github.com/kkapel/GophProfile/internal/metrics"
	"github.com/kkapel/GophProfile/internal/repository"
	"github.com/kkapel/GophProfile/internal/storage"
)

// MaxFileSize — максимальный размер загружаемого файла (10 MB).
const MaxFileSize int64 = 10 << 20

// Ошибки бизнес-логики.
var (
	// ErrNotFound — аватарка не найдена.
	ErrNotFound = errors.New("avatar not found")
	// ErrForbidden — попытка изменить чужую аватарку.
	ErrForbidden = errors.New("forbidden")
	// ErrFileTooLarge — файл превышает MaxFileSize.
	ErrFileTooLarge = errors.New("file too large")
	// ErrUnsupportedFormat — неподдерживаемый формат изображения.
	ErrUnsupportedFormat = errors.New("unsupported file format")
)

// allowedMimeTypes — форматы, которые сервис принимает к загрузке.
var allowedMimeTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

// EventPublisher публикует события в брокер сообщений.
// Реализация на RabbitMQ появится на следующем этапе.
type EventPublisher interface {
	// Publish отправляет событие с указанным routing key.
	Publish(ctx context.Context, routingKey string, event any) error
}

// UploadInput — входные данные для загрузки аватарки.
type UploadInput struct {
	UserID   string
	FileName string
	Size     int64
	// File — содержимое файла. ReadSeeker нужен, чтобы определить
	// MIME-тип по первым байтам и затем вернуться в начало.
	File io.ReadSeeker
}

// AvatarRepository — доступ к метаданным аватарок, необходимый сервису.
type AvatarRepository interface {
	Create(ctx context.Context, avatar domain.Avatar) (domain.Avatar, error)
	GetByID(ctx context.Context, id uuid.UUID) (domain.Avatar, error)
	GetLatestByUserID(ctx context.Context, userID string) (domain.Avatar, error)
	ListByUserID(ctx context.Context, userID string) ([]domain.Avatar, error)
	SoftDelete(ctx context.Context, id uuid.UUID) (domain.Avatar, error)
}

// FileStorage — операции над файлами, необходимые сервису.
type FileStorage interface {
	Upload(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Download(ctx context.Context, key string) (*storage.Object, error)
	Delete(ctx context.Context, key string) error
}

// AvatarService реализует бизнес-логику работы с аватарками.
type AvatarService struct {
	repo      AvatarRepository
	storage   FileStorage
	publisher EventPublisher
	log       *slog.Logger
	metrics   *metrics.Metrics
	tracer    trace.Tracer
}

// NewAvatarService создаёт сервис аватарок.
func NewAvatarService(
	repo AvatarRepository,
	store FileStorage,
	publisher EventPublisher,
	log *slog.Logger,
	metrics *metrics.Metrics,
) *AvatarService {
	return &AvatarService{repo: repo, storage: store, publisher: publisher, log: log, metrics: metrics, tracer: otel.Tracer("gophprofile/services")}
}

// Upload сохраняет оригинал в хранилище, создаёт запись в БД
// и публикует событие на асинхронную обработку.
func (s *AvatarService) Upload(ctx context.Context, in UploadInput) (avatar domain.Avatar, err error) {
	ctx, span := s.tracer.Start(ctx, "avatar.upload")
	defer span.End()

	span.SetAttributes(
		attribute.String("user_id", in.UserID),
		attribute.String("file_name", in.FileName),
		attribute.Int64("file_size", in.Size),
	)

	start := time.Now()

	// Итог операции известен только на выходе, поэтому пишем метрики в defer.
	defer func() {
		status := "success"
		if err != nil {
			status = "error"
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}

		s.metrics.AvatarUploadsTotal.WithLabelValues(status).Inc()
		s.metrics.AvatarUploadDuration.WithLabelValues(status).Observe(time.Since(start).Seconds())
	}()

	if in.Size > MaxFileSize {
		return domain.Avatar{}, ErrFileTooLarge
	}

	mimeType, ext, err := detectMimeType(in.File)
	if err != nil {
		return domain.Avatar{}, err
	}

	avatarID := uuid.New()
	key := storage.OriginalKey(avatarID.String(), ext)

	s.log.InfoContext(ctx, "uploading avatar",
		"avatar_id", avatarID,
		"user_id", in.UserID,
		"file_name", in.FileName,
		"file_size", in.Size,
		"mime_type", mimeType,
	)

	if err := s.storage.Upload(ctx, key, in.File, in.Size, mimeType); err != nil {
		return domain.Avatar{}, fmt.Errorf("upload to storage: %w", err)
	}

	avatar, err = s.repo.Create(ctx, domain.Avatar{
		ID:               avatarID,
		UserID:           in.UserID,
		FileName:         in.FileName,
		MimeType:         mimeType,
		SizeBytes:        in.Size,
		S3Key:            key,
		UploadStatus:     domain.UploadStatusUploaded,
		ProcessingStatus: domain.ProcessingStatusPending,
	})
	if err != nil {
		// Файл уже в хранилище — убираем его, чтобы не оставлять мусор.
		if delErr := s.storage.Delete(ctx, key); delErr != nil {
			s.log.ErrorContext(ctx, "cleanup uploaded file", "s3_key", key, "err", delErr)
		}
		return domain.Avatar{}, err
	}

	event := domain.AvatarUploadEvent{
		EventID:  uuid.NewString(),
		AvatarID: avatarID.String(),
		UserID:   in.UserID,
		S3Key:    key,
	}
	if err := s.publisher.Publish(ctx, domain.EventAvatarUploaded, event); err != nil {
		// Запись создана и файл загружен, поэтому запрос считаем успешным.
		// Миниатюры останутся в статусе pending до повторной обработки.
		s.log.ErrorContext(ctx, "publish upload event",
			"avatar_id", avatarID, "err", err)
		return avatar, nil
	}

	s.metrics.AvatarUploadSize.Observe(float64(in.Size))

	s.log.InfoContext(ctx, "avatar accepted",
		"avatar_id", avatarID, "user_id", in.UserID, "s3_key", key)

	return avatar, nil
}

// GetFile возвращает файл аватарки нужного размера.
func (s *AvatarService) GetFile(ctx context.Context, id uuid.UUID, size string) (*storage.Object, error) {
	avatar, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, mapRepoError(err)
	}

	return s.downloadBySize(ctx, avatar, size)
}

// GetUserFile возвращает файл последней аватарки пользователя.
func (s *AvatarService) GetUserFile(ctx context.Context, userID, size string) (*storage.Object, error) {
	avatar, err := s.repo.GetLatestByUserID(ctx, userID)
	if err != nil {
		return nil, mapRepoError(err)
	}

	return s.downloadBySize(ctx, avatar, size)
}

// downloadBySize выбирает нужный ключ (оригинал или миниатюру) и качает файл.
// Если миниатюра ещё не готова, отдаёт оригинал.
func (s *AvatarService) downloadBySize(ctx context.Context, avatar domain.Avatar, size string) (*storage.Object, error) {
	key := avatar.S3Key
	if size != "" && size != "original" {
		if thumbKey, ok := avatar.ThumbnailS3Keys[size]; ok {
			key = thumbKey
		}
	}

	obj, err := s.storage.Download(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("download avatar file: %w", err)
	}

	return obj, nil
}

// GetMetadata возвращает метаданные аватарки.
func (s *AvatarService) GetMetadata(ctx context.Context, id uuid.UUID) (domain.Avatar, error) {
	avatar, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return domain.Avatar{}, mapRepoError(err)
	}

	return avatar, nil
}

// List возвращает все аватарки пользователя.
func (s *AvatarService) List(ctx context.Context, userID string) ([]domain.Avatar, error) {
	return s.repo.ListByUserID(ctx, userID)
}

// Delete помечает аватарку удалённой и публикует событие на удаление файлов.
func (s *AvatarService) Delete(ctx context.Context, id uuid.UUID, requesterID string) (err error) {
	ctx, span := s.tracer.Start(ctx, "avatar.delete")
	defer span.End()

	span.SetAttributes(
		attribute.String("avatar_id", id.String()),
		attribute.String("requester_id", requesterID),
	)

	defer func() {
		status := "success"
		if err != nil {
			status = "error"
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}

		s.metrics.AvatarDeletesTotal.WithLabelValues(status).Inc()
	}()

	avatar, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return mapRepoError(err)
	}

	if avatar.UserID != requesterID {
		s.log.WarnContext(ctx, "avatar delete forbidden",
			"avatar_id", id, "owner_id", avatar.UserID, "requester_id", requesterID)

		return ErrForbidden
	}

	deleted, err := s.repo.SoftDelete(ctx, id)
	if err != nil {
		return mapRepoError(err)
	}

	s.publishDeleteEvent(ctx, deleted)

	s.log.InfoContext(ctx, "avatar deleted", "avatar_id", id, "user_id", requesterID)

	return nil
}

// DeleteUserAvatar удаляет последнюю аватарку пользователя.
func (s *AvatarService) DeleteUserAvatar(ctx context.Context, userID, requesterID string) error {
	if userID != requesterID {
		return ErrForbidden
	}

	avatar, err := s.repo.GetLatestByUserID(ctx, userID)
	if err != nil {
		return mapRepoError(err)
	}

	return s.Delete(ctx, avatar.ID, requesterID)
}

// publishDeleteEvent отправляет событие на удаление файлов из хранилища.
// Ошибка публикации не влияет на результат запроса: запись уже помечена
// удалённой и недоступна через API.
func (s *AvatarService) publishDeleteEvent(ctx context.Context, avatar domain.Avatar) {
	keys := make([]string, 0, len(avatar.ThumbnailS3Keys)+1)
	keys = append(keys, avatar.S3Key)
	for _, key := range avatar.ThumbnailS3Keys {
		keys = append(keys, key)
	}

	err := s.publisher.Publish(ctx, domain.EventAvatarDeleted, domain.AvatarDeleteEvent{
		EventID:  uuid.NewString(),
		AvatarID: avatar.ID.String(),
		S3Keys:   keys,
	})
	if err != nil {
		s.log.ErrorContext(ctx, "publish delete event", "avatar_id", avatar.ID, "err", err)
	}
}

// detectMimeType определяет тип файла по первым байтам (magic bytes)
// и возвращает MIME-тип с соответствующим расширением.
// После проверки возвращает курсор в начало файла.
func detectMimeType(file io.ReadSeeker) (mimeType, ext string, err error) {
	buf := make([]byte, 512)
	n, err := file.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", "", fmt.Errorf("read file header: %w", err)
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", "", fmt.Errorf("seek file: %w", err)
	}

	// DetectContentType анализирует сигнатуру файла, а не имя,
	// поэтому подделать расширением не получится.
	mimeType = http.DetectContentType(buf[:n])

	ext, ok := allowedMimeTypes[mimeType]
	if !ok {
		return "", "", ErrUnsupportedFormat
	}

	return mimeType, ext, nil
}

// mapRepoError переводит ошибки репозитория в ошибки сервисного слоя.
func mapRepoError(err error) error {
	if errors.Is(err, repository.ErrNotFound) {
		return ErrNotFound
	}

	return err
}
