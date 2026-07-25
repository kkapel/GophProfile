// Package repository предоставляет доступ к метаданным аватарок в PostgreSQL.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kkapel/GophProfile/internal/domain"
	"github.com/kkapel/GophProfile/internal/repository/db"
)

// ErrNotFound возвращается, когда аватарка не найдена или уже удалена.
var ErrNotFound = errors.New("avatar not found")

// AvatarRepository описывает операции над метаданными аватарок.
type AvatarRepository interface {
	// Create сохраняет метаданные новой аватарки и возвращает созданную запись.
	Create(ctx context.Context, avatar domain.Avatar) (domain.Avatar, error)

	// GetByID возвращает аватарку по идентификатору.
	// Если запись отсутствует или помечена удалённой, возвращает ErrNotFound.
	GetByID(ctx context.Context, id uuid.UUID) (domain.Avatar, error)

	// GetLatestByUserID возвращает последнюю загруженную аватарку пользователя.
	// Если у пользователя нет аватарок, возвращает ErrNotFound.
	GetLatestByUserID(ctx context.Context, userID string) (domain.Avatar, error)

	// ListByUserID возвращает все неудалённые аватарки пользователя,
	// отсортированные от новых к старым.
	ListByUserID(ctx context.Context, userID string) ([]domain.Avatar, error)

	// SoftDelete помечает аватарку удалённой и возвращает её последнее состояние.
	// Повторное удаление возвращает ErrNotFound.
	SoftDelete(ctx context.Context, id uuid.UUID) (domain.Avatar, error)

	// UpdateProcessingResult сохраняет результат асинхронной обработки:
	// статус, ключи миниатюр в S3 и размеры исходного изображения.
	UpdateProcessingResult(ctx context.Context, id uuid.UUID, status string,
		thumbnails map[string]string, width, height int32) (domain.Avatar, error)

	// UpdateProcessingStatus обновляет статус обработки изображения.
	UpdateProcessingStatus(ctx context.Context, id uuid.UUID, status string) error

	// UpdateUploadStatus обновляет статус загрузки файла в хранилище.
	UpdateUploadStatus(ctx context.Context, id uuid.UUID, status string) error
}

// avatarRepository — реализация AvatarRepository поверх сгенерированных
// sqlc-запросов и пула подключений pgx.
type avatarRepository struct {
	queries *db.Queries
}

// NewAvatarRepository создаёт репозиторий поверх пула подключений.
func NewAvatarRepository(pool *pgxpool.Pool) AvatarRepository {
	return &avatarRepository{queries: db.New(pool)}
}

// Create сохраняет метаданные новой аватарки и возвращает созданную запись.
func (r *avatarRepository) Create(ctx context.Context, avatar domain.Avatar) (domain.Avatar, error) {
	row, err := r.queries.CreateAvatar(ctx, db.CreateAvatarParams{
		ID:               avatar.ID,
		UserID:           avatar.UserID,
		FileName:         avatar.FileName,
		MimeType:         avatar.MimeType,
		SizeBytes:        avatar.SizeBytes,
		S3Key:            avatar.S3Key,
		UploadStatus:     avatar.UploadStatus,
		ProcessingStatus: avatar.ProcessingStatus,
	})
	if err != nil {
		return domain.Avatar{}, fmt.Errorf("create avatar: %w", err)
	}

	return toDomain(row)
}

// GetByID возвращает аватарку по идентификатору.
// Если запись отсутствует или помечена удалённой, возвращает ErrNotFound.
func (r *avatarRepository) GetByID(ctx context.Context, id uuid.UUID) (domain.Avatar, error) {
	row, err := r.queries.GetAvatarByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Avatar{}, ErrNotFound
		}
		return domain.Avatar{}, fmt.Errorf("get avatar by id: %w", err)
	}

	return toDomain(row)
}

// GetLatestByUserID возвращает последнюю загруженную аватарку пользователя.
// Если у пользователя нет аватарок, возвращает ErrNotFound.
func (r *avatarRepository) GetLatestByUserID(ctx context.Context, userID string) (domain.Avatar, error) {
	row, err := r.queries.GetLatestAvatarByUserID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Avatar{}, ErrNotFound
		}
		return domain.Avatar{}, fmt.Errorf("get latest avatar: %w", err)
	}

	return toDomain(row)
}

// ListByUserID возвращает все неудалённые аватарки пользователя,
// отсортированные от новых к старым.
func (r *avatarRepository) ListByUserID(ctx context.Context, userID string) ([]domain.Avatar, error) {
	rows, err := r.queries.ListAvatarsByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list avatars: %w", err)
	}

	avatars := make([]domain.Avatar, 0, len(rows))
	for _, row := range rows {
		avatar, err := toDomain(row)
		if err != nil {
			return nil, err
		}
		avatars = append(avatars, avatar)
	}

	return avatars, nil
}

// SoftDelete помечает аватарку удалённой и возвращает её последнее состояние.
// Повторное удаление возвращает ErrNotFound.
func (r *avatarRepository) SoftDelete(ctx context.Context, id uuid.UUID) (domain.Avatar, error) {
	row, err := r.queries.SoftDeleteAvatar(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Avatar{}, ErrNotFound
		}
		return domain.Avatar{}, fmt.Errorf("soft delete avatar: %w", err)
	}

	return toDomain(row)
}

// UpdateProcessingResult сохраняет результат асинхронной обработки:
// статус, ключи миниатюр в S3 и размеры исходного изображения.
func (r *avatarRepository) UpdateProcessingResult(ctx context.Context, id uuid.UUID, status string,
	thumbnails map[string]string, width, height int32,
) (domain.Avatar, error) {
	raw, err := json.Marshal(thumbnails)
	if err != nil {
		return domain.Avatar{}, fmt.Errorf("marshal thumbnails: %w", err)
	}

	row, err := r.queries.UpdateProcessingResult(ctx, db.UpdateProcessingResultParams{
		ID:               id,
		ProcessingStatus: status,
		ThumbnailS3Keys:  raw,
		Width:            &width,
		Height:           &height,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Avatar{}, ErrNotFound
		}
		return domain.Avatar{}, fmt.Errorf("update processing result: %w", err)
	}

	return toDomain(row)
}

// UpdateProcessingStatus обновляет статус обработки изображения.
func (r *avatarRepository) UpdateProcessingStatus(ctx context.Context, id uuid.UUID, status string) error {
	if err := r.queries.UpdateProcessingStatus(ctx, db.UpdateProcessingStatusParams{
		ID:               id,
		ProcessingStatus: status,
	}); err != nil {
		return fmt.Errorf("update processing status: %w", err)
	}

	return nil
}

// UpdateUploadStatus обновляет статус загрузки файла в хранилище.
func (r *avatarRepository) UpdateUploadStatus(ctx context.Context, id uuid.UUID, status string) error {
	if err := r.queries.UpdateUploadStatus(ctx, db.UpdateUploadStatusParams{
		ID:           id,
		UploadStatus: status,
	}); err != nil {
		return fmt.Errorf("update upload status: %w", err)
	}

	return nil
}

// toDomain переводит сгенерированную sqlc-модель в доменную,
// распаковывая JSONB с ключами миниатюр.
func toDomain(row db.Avatar) (domain.Avatar, error) {
	thumbnails := make(map[string]string)
	if len(row.ThumbnailS3Keys) > 0 {
		if err := json.Unmarshal(row.ThumbnailS3Keys, &thumbnails); err != nil {
			return domain.Avatar{}, fmt.Errorf("unmarshal thumbnails: %w", err)
		}
	}

	return domain.Avatar{
		ID:               row.ID,
		UserID:           row.UserID,
		FileName:         row.FileName,
		MimeType:         row.MimeType,
		SizeBytes:        row.SizeBytes,
		Width:            row.Width,
		Height:           row.Height,
		S3Key:            row.S3Key,
		ThumbnailS3Keys:  thumbnails,
		UploadStatus:     row.UploadStatus,
		ProcessingStatus: row.ProcessingStatus,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
		DeletedAt:        row.DeletedAt,
	}, nil
}
