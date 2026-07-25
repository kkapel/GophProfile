package domain

// Имена событий, публикуемых в брокер сообщений.
const (
	EventAvatarUploaded = "avatar.uploaded"
	EventAvatarDeleted  = "avatar.deleted"
)

// AvatarUploadEvent — событие о загрузке оригинала аватарки.
// Worker по нему создаёт миниатюры.
type AvatarUploadEvent struct {
	// EventID — уникальный идентификатор сообщения для идемпотентности.
	EventID  string `json:"event_id"`
	AvatarID string `json:"avatar_id"`
	UserID   string `json:"user_id"`
	S3Key    string `json:"s3_key"`
}

// AvatarDeleteEvent — событие об удалении аватарки.
// Worker по нему удаляет файлы из хранилища.
type AvatarDeleteEvent struct {
	EventID  string   `json:"event_id"`
	AvatarID string   `json:"avatar_id"`
	S3Keys   []string `json:"s3_keys"`
}
