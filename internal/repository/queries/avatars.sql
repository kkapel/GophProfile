-- name: CreateAvatar :one
INSERT INTO avatars (
    id, user_id, file_name, mime_type, size_bytes, s3_key, upload_status, processing_status
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
)
RETURNING *;

-- name: GetAvatarByID :one
SELECT * FROM avatars
WHERE id = $1 AND deleted_at IS NULL;

-- name: GetLatestAvatarByUserID :one
SELECT * FROM avatars
WHERE user_id = $1 AND deleted_at IS NULL
ORDER BY created_at DESC
LIMIT 1;

-- name: ListAvatarsByUserID :many
SELECT * FROM avatars
WHERE user_id = $1 AND deleted_at IS NULL
ORDER BY created_at DESC;

-- name: SoftDeleteAvatar :one
UPDATE avatars
SET deleted_at = NOW(), updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: UpdateProcessingResult :one
UPDATE avatars
SET processing_status = $2,
    thumbnail_s3_keys = $3,
    width = $4,
    height = $5,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: UpdateProcessingStatus :exec
UPDATE avatars
SET processing_status = $2, updated_at = NOW()
WHERE id = $1;

-- name: UpdateUploadStatus :exec
UPDATE avatars
SET upload_status = $2, updated_at = NOW()
WHERE id = $1;