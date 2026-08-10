-- name: CreateAvatar :one
INSERT INTO avatars (
    id, user_id, file_name, mime_type, size_bytes, s3_key, upload_status, processing_status
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
)
RETURNING id, user_id, file_name, mime_type, size_bytes, width, height,
          s3_key, thumbnail_s3_keys, upload_status, processing_status,
          created_at, updated_at, deleted_at;

-- name: GetAvatarByID :one
SELECT id, user_id, file_name, mime_type, size_bytes, width, height,
       s3_key, thumbnail_s3_keys, upload_status, processing_status,
       created_at, updated_at, deleted_at
FROM avatars
WHERE id = $1 AND deleted_at IS NULL;

-- name: GetLatestAvatarByUserID :one
SELECT id, user_id, file_name, mime_type, size_bytes, width, height,
       s3_key, thumbnail_s3_keys, upload_status, processing_status,
       created_at, updated_at, deleted_at
FROM avatars
WHERE user_id = $1 AND deleted_at IS NULL
ORDER BY created_at DESC
LIMIT 1;

-- name: ListAvatarsByUserID :many
SELECT id, user_id, file_name, mime_type, size_bytes, width, height,
       s3_key, thumbnail_s3_keys, upload_status, processing_status,
       created_at, updated_at, deleted_at
FROM avatars
WHERE user_id = $1 AND deleted_at IS NULL
ORDER BY created_at DESC;

-- name: SoftDeleteAvatar :one
UPDATE avatars
SET deleted_at = NOW(), updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL
RETURNING id, user_id, file_name, mime_type, size_bytes, width, height,
          s3_key, thumbnail_s3_keys, upload_status, processing_status,
          created_at, updated_at, deleted_at;

-- name: UpdateProcessingResult :one
UPDATE avatars
SET processing_status = $2,
    thumbnail_s3_keys = $3,
    width = $4,
    height = $5,
    updated_at = NOW()
WHERE id = $1
RETURNING id, user_id, file_name, mime_type, size_bytes, width, height,
          s3_key, thumbnail_s3_keys, upload_status, processing_status,
          created_at, updated_at, deleted_at;

-- name: UpdateProcessingStatus :exec
UPDATE avatars
SET processing_status = $2, updated_at = NOW()
WHERE id = $1;

-- name: UpdateUploadStatus :exec
UPDATE avatars
SET upload_status = $2, updated_at = NOW()
WHERE id = $1;

-- name: TotalStorageBytes :one
SELECT COALESCE(SUM(size_bytes), 0)::BIGINT FROM avatars
WHERE deleted_at IS NULL;