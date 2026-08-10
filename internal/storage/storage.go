// Package storage предоставляет доступ к S3-совместимому хранилищу файлов.
package storage

import (
	"context"
	"fmt"
	"io"

	// minio-go — официальный Go-клиент для MinIO и любого S3-совместимого хранилища.
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Object — файл, полученный из хранилища.
type Object struct {
	// Body — поток байтов файла.
	Body io.ReadCloser
	// ContentType — MIME-тип
	// Нужен, чтобы отдать правильный заголовок в HTTP-ответе.
	ContentType string
	// Size — размер файла в байтах, приходит из метаданных объекта.
	Size int64
}

// Storage — объектное хранилище, совместимое с S3.
type Storage struct {
	client *minio.Client
	bucket string
	tracer trace.Tracer
}

// Config — параметры подключения к хранилищу.
type Config struct {
	Endpoint  string // адрес S3 API, например "localhost:9000" (БЕЗ схемы http://)
	AccessKey string // логин (в AWS это Access Key ID)
	SecretKey string // пароль (в AWS это Secret Access Key)
	Bucket    string // имя бакета
	UseSSL    bool   // true — ходить по https, false — по http (локально false)
}

// New создаёт клиент хранилища и при необходимости создаёт бакет.
func New(ctx context.Context, cfg Config) (*Storage, error) {
	// minio.New только конструирует клиента и валидирует параметры —
	// сетевого подключения здесь ещё НЕ происходит.
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		// NewStaticV4 — статические ключи, подпись запросов по алгоритму AWS Signature V4.
		// Третий аргумент — session token, нужен только для временных credentials AWS.
		Creds: credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		// Secure=false => http://, true => https://. Схему в Endpoint писать не надо.
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("create minio client: %w", err)
	}

	s := &Storage{client: client, bucket: cfg.Bucket, tracer: otel.Tracer("gophprofile/storage")}

	// Первый реальный поход в сеть.
	if err := s.ensureBucket(ctx); err != nil {
		return nil, err
	}

	return s, nil
}

// ensureBucket создаёт бакет, если он ещё не существует.
// Без этого первая же загрузка упадёт с ошибкой NoSuchBucket.
func (s *Storage) ensureBucket(ctx context.Context) error {
	// BucketExists делает HEAD-запрос к бакету.
	// Ошибка здесь означает проблему с сетью или доступом, а не отсутствие бакета.
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("check bucket %q: %w", s.bucket, err)
	}
	if exists {
		return nil // бакет уже есть — ничего не делаем
	}

	// MakeBucketOptions{} — дефолтные настройки: регион по умолчанию,
	// без object locking. Для локального MinIO этого достаточно.
	if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); err != nil {
		return fmt.Errorf("create bucket %q: %w", s.bucket, err)
	}

	return nil
}

// Upload сохраняет объект по указанному ключу.
// key — это полное имя объекта, например "avatars/<uuid>/original.jpg".
// Слэши в нём — просто часть имени, настоящих папок в S3 нет.
func (s *Storage) Upload(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	ctx, span := s.tracer.Start(ctx, "storage.Upload",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("s3.bucket", s.bucket),
			attribute.String("s3.key", key),
			attribute.Int64("s3.size", size),
			attribute.String("s3.content_type", contentType),
		),
	)
	defer span.End()

	// PutObject читает данные из r и заливает их в бакет.
	// size — ожидаемый размер в байтах. Если он неизвестен, передают -1,
	// тогда SDK грузит файл частями (multipart) и потребляет больше памяти.
	// У нас размер известен из multipart-формы, поэтому передаём точное значение.
	// Первый возвращаемый аргумент (UploadInfo с ETag и версией) нам не нужен.
	_, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{
		// ContentType сохраняется в метаданных объекта и вернётся при скачивании.
		// Без него S3 проставит application/octet-stream, и браузер не покажет картинку.
		ContentType: contentType,
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "upload failed")
		return fmt.Errorf("upload object %q: %w", key, err)
	}

	return nil
}

// Download возвращает объект по ключу.
func (s *Storage) Download(ctx context.Context, key string) (*Object, error) {
	ctx, span := s.tracer.Start(ctx, "storage.Download",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("s3.bucket", s.bucket),
			attribute.String("s3.key", key),
		),
	)
	defer span.End()

	// ВАЖНО: GetObject ленивый — он НЕ делает сетевой запрос и почти никогда
	// не возвращает ошибку здесь. Он лишь готовит объект-ридер.
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "download failed")
		return nil, fmt.Errorf("get object %q: %w", key, err)
	}

	// Stat вызывает реальный запрос к хранилищу и возвращает метаданные.
	// Именно тут выяснится, что объекта нет (ошибка с кодом NoSuchKey).
	// Без Stat мы узнали бы об этом только при первом Read — слишком поздно.
	info, err := obj.Stat()
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "download failed")
		// Ридер надо закрыть, раз наружу мы его не отдаём.
		_ = obj.Close()
		return nil, fmt.Errorf("stat object %q: %w", key, err)
	}
	span.SetAttributes(attribute.Int64("s3.size", info.Size))

	return &Object{
		Body:        obj, // minio.Object реализует io.ReadCloser (и io.Seeker)
		ContentType: info.ContentType,
		Size:        info.Size,
	}, nil
}

// Delete удаляет объект по ключу.
func (s *Storage) Delete(ctx context.Context, key string) error {
	ctx, span := s.tracer.Start(ctx, "storage.Delete",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("s3.bucket", s.bucket),
			attribute.String("s3.key", key),
		),
	)
	defer span.End()

	// RemoveObject идемпотентен: удаление несуществующего ключа
	// не считается ошибкой в S3. Это удобно для повторной обработки
	// одного и того же события из очереди.
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "delete failed")

		return fmt.Errorf("remove object %q: %w", key, err)
	}

	return nil
}

// DeleteMany удаляет несколько объектов.
// Здесь простой цикл: объектов у нас максимум три (оригинал + две миниатюры).
// Для массовых удалений в SDK есть RemoveObjects с каналом ключей.
func (s *Storage) DeleteMany(ctx context.Context, keys []string) error {
	ctx, span := s.tracer.Start(ctx, "storage.DeleteMany",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.Int("s3.keys_count", len(keys))),
	)
	defer span.End()

	for _, key := range keys {
		if err := s.Delete(ctx, key); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "delete many failed")

			return err
		}
	}

	return nil
}

// Ping проверяет доступность хранилища.
// Отдельного health-метода в SDK нет, поэтому используем самый дешёвый
// запрос — проверку существования бакета. Заодно проверяются сеть и credentials.
func (s *Storage) Ping(ctx context.Context) error {
	if _, err := s.client.BucketExists(ctx, s.bucket); err != nil {
		return fmt.Errorf("ping storage: %w", err)
	}

	return nil
}
