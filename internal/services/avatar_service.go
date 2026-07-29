// Package services содержит бизнес-логику сервиса аватарок.
package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/google/uuid"

	"github.com/kkapel/GophProfile/internal/domain"
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

// AvatarService описывает операции над аватарками.
type AvatarService interface {
	// Upload сохраняет оригинал в хранилище, создаёт запись в БД
	// и публикует событие на асинхронную обработку.
	Upload(ctx context.Context, in UploadInput) (domain.Avatar, error)

	// GetFile возвращает файл аватарки нужного размера.
	// Вызывающий обязан закрыть Object.Body.
	GetFile(ctx context.Context, id uuid.UUID, size string) (*storage.Object, error)

	// GetUserFile возвращает файл последней аватарки пользователя.
	GetUserFile(ctx context.Context, userID, size string) (*storage.Object, error)

	// GetMetadata возвращает метаданные аватарки.
	GetMetadata(ctx context.Context, id uuid.UUID) (domain.Avatar, error)

	// List возвращает все аватарки пользователя.
	List(ctx context.Context, userID string) ([]domain.Avatar, error)

	// Delete помечает аватарку удалённой и публикует событие
	// на асинхронное удаление файлов. Удалять может только владелец.
	Delete(ctx context.Context, id uuid.UUID, requesterID string) error

	// DeleteUserAvatar удаляет последнюю аватарку пользователя.
	DeleteUserAvatar(ctx context.Context, userID, requesterID string) error
}

// avatarService — реализация AvatarService.
type avatarService struct {
	repo      repository.AvatarRepository
	storage   storage.FileStorage
	publisher EventPublisher
}

// NewAvatarService создаёт сервис аватарок.
func NewAvatarService(
	repo repository.AvatarRepository,
	store storage.FileStorage,
	publisher EventPublisher,
) *avatarService {
	return &avatarService{repo: repo, storage: store, publisher: publisher}
}

// Upload сохраняет оригинал в хранилище, создаёт запись в БД
// и публикует событие на асинхронную обработку.
func (s *avatarService) Upload(ctx context.Context, in UploadInput) (domain.Avatar, error) {
	if in.Size > MaxFileSize {
		return domain.Avatar{}, ErrFileTooLarge
	}

	mimeType, ext, err := detectMimeType(in.File)
	if err != nil {
		return domain.Avatar{}, err
	}

	avatarID := uuid.New()
	key := storage.OriginalKey(avatarID.String(), ext)

	if err := s.storage.Upload(ctx, key, in.File, in.Size, mimeType); err != nil {
		return domain.Avatar{}, fmt.Errorf("upload to storage: %w", err)
	}

	avatar, err := s.repo.Create(ctx, domain.Avatar{
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
		_ = s.storage.Delete(ctx, key)
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
		return avatar, nil
	}

	return avatar, nil
}

// GetFile возвращает файл аватарки нужного размера.
func (s *avatarService) GetFile(ctx context.Context, id uuid.UUID, size string) (*storage.Object, error) {
	avatar, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, mapRepoError(err)
	}

	return s.downloadBySize(ctx, avatar, size)
}

// GetUserFile возвращает файл последней аватарки пользователя.
func (s *avatarService) GetUserFile(ctx context.Context, userID, size string) (*storage.Object, error) {
	avatar, err := s.repo.GetLatestByUserID(ctx, userID)
	if err != nil {
		return nil, mapRepoError(err)
	}

	return s.downloadBySize(ctx, avatar, size)
}

// downloadBySize выбирает нужный ключ (оригинал или миниатюру) и качает файл.
// Если миниатюра ещё не готова, отдаёт оригинал.
func (s *avatarService) downloadBySize(ctx context.Context, avatar domain.Avatar, size string) (*storage.Object, error) {
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
func (s *avatarService) GetMetadata(ctx context.Context, id uuid.UUID) (domain.Avatar, error) {
	avatar, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return domain.Avatar{}, mapRepoError(err)
	}

	return avatar, nil
}

// List возвращает все аватарки пользователя.
func (s *avatarService) List(ctx context.Context, userID string) ([]domain.Avatar, error) {
	return s.repo.ListByUserID(ctx, userID)
}

// Delete помечает аватарку удалённой и публикует событие на удаление файлов.
func (s *avatarService) Delete(ctx context.Context, id uuid.UUID, requesterID string) error {
	avatar, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return mapRepoError(err)
	}

	if avatar.UserID != requesterID {
		return ErrForbidden
	}

	deleted, err := s.repo.SoftDelete(ctx, id)
	if err != nil {
		return mapRepoError(err)
	}

	s.publishDeleteEvent(ctx, deleted)

	return nil
}

// DeleteUserAvatar удаляет последнюю аватарку пользователя.
func (s *avatarService) DeleteUserAvatar(ctx context.Context, userID, requesterID string) error {
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
func (s *avatarService) publishDeleteEvent(ctx context.Context, avatar domain.Avatar) {
	keys := make([]string, 0, len(avatar.ThumbnailS3Keys)+1)
	keys = append(keys, avatar.S3Key)
	for _, key := range avatar.ThumbnailS3Keys {
		keys = append(keys, key)
	}

	_ = s.publisher.Publish(ctx, domain.EventAvatarDeleted, domain.AvatarDeleteEvent{
		EventID:  uuid.NewString(),
		AvatarID: avatar.ID.String(),
		S3Keys:   keys,
	})
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
