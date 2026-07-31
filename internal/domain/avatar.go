// Package domain содержит доменные модели сервиса.
package domain

import (
	"time"

	"github.com/google/uuid"
)

// Статусы загрузки файла.
const (
	UploadStatusUploading = "uploading"
	UploadStatusUploaded  = "uploaded"
	UploadStatusFailed    = "failed"
)

// Статусы асинхронной обработки изображения.
const (
	ProcessingStatusPending   = "pending"
	ProcessingStatusRunning   = "processing"
	ProcessingStatusCompleted = "completed"
	ProcessingStatusFailed    = "failed"
)

// Размеры генерируемых миниатюр.
const (
	ThumbSize100 = "100x100"
	ThumbSize300 = "300x300"
)

// Avatar — метаданные загруженной аватарки.
type Avatar struct {
	ID               uuid.UUID
	UserID           string
	FileName         string
	MimeType         string
	SizeBytes        int64
	Width            *int32
	Height           *int32
	S3Key            string
	ThumbnailS3Keys  map[string]string
	UploadStatus     string
	ProcessingStatus string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        *time.Time
}
