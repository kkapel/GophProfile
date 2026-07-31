package handlers

import (
	"fmt"

	"github.com/kkapel/GophProfile/internal/api"
	"github.com/kkapel/GophProfile/internal/domain"
)

// toAPIAvatar переводит доменную модель в ответ API.
func toAPIAvatar(avatar domain.Avatar) api.Avatar {
	return api.Avatar{
		Id:        avatar.ID,
		UserId:    avatar.UserID,
		Url:       avatarURL(avatar.ID.String()),
		Status:    toAPIStatus(avatar.ProcessingStatus),
		CreatedAt: avatar.CreatedAt,
	}
}

// toAPIMetadata переводит доменную модель в подробные метаданные.
func toAPIMetadata(avatar domain.Avatar) api.AvatarMetadata {
	meta := api.AvatarMetadata{
		Id:        avatar.ID,
		UserId:    avatar.UserID,
		FileName:  avatar.FileName,
		MimeType:  avatar.MimeType,
		Size:      avatar.SizeBytes,
		CreatedAt: avatar.CreatedAt,
		UpdatedAt: avatar.UpdatedAt,
	}

	if avatar.Width != nil && avatar.Height != nil {
		meta.Dimensions = &api.Dimensions{
			Width:  int(*avatar.Width),
			Height: int(*avatar.Height),
		}
	}

	if len(avatar.ThumbnailS3Keys) > 0 {
		thumbnails := make([]api.Thumbnail, 0, len(avatar.ThumbnailS3Keys))
		// Порядок фиксируем явно, чтобы ответ был стабильным.
		for _, size := range []string{domain.ThumbSize100, domain.ThumbSize300} {
			if _, ok := avatar.ThumbnailS3Keys[size]; ok {
				thumbnails = append(thumbnails, api.Thumbnail{
					Size: size,
					Url:  fmt.Sprintf("%s?size=%s", avatarURL(avatar.ID.String()), size),
				})
			}
		}
		meta.Thumbnails = &thumbnails
	}

	return meta
}

// toAPIStatus переводит внутренний статус обработки в статус ответа API.
func toAPIStatus(processingStatus string) api.AvatarStatus {
	switch processingStatus {
	case domain.ProcessingStatusCompleted:
		return api.Completed
	case domain.ProcessingStatusFailed:
		return api.Failed
	default:
		return api.Processing
	}
}

// avatarURL строит публичную ссылку на файл аватарки.
func avatarURL(avatarID string) string {
	return fmt.Sprintf("/api/v1/avatars/%s", avatarID)
}
