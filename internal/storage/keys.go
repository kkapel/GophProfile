package storage

import "fmt"

// OriginalKey возвращает ключ оригинального файла аватарки.
// Пример: avatars/550e8400-e29b-41d4-a716-446655440000/original.jpg
func OriginalKey(avatarID, ext string) string {
	return fmt.Sprintf("avatars/%s/original%s", avatarID, ext)
}

// ThumbnailKey возвращает ключ миниатюры заданного размера.
// Пример: thumbnails/550e8400-e29b-41d4-a716-446655440000/100x100.jpg
// Миниатюры всегда сохраняем в JPEG.
func ThumbnailKey(avatarID, size string) string {
	return fmt.Sprintf("thumbnails/%s/%s.jpg", avatarID, size)
}
