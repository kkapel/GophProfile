// Команда worker обрабатывает события из брокера сообщений:
// создаёт миниатюры аватарок и удаляет файлы из хранилища.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kkapel/GophProfile/internal/broker"
	"github.com/kkapel/GophProfile/internal/config"
	"github.com/kkapel/GophProfile/internal/database"
	"github.com/kkapel/GophProfile/internal/logger"
	"github.com/kkapel/GophProfile/internal/metrics"
	"github.com/kkapel/GophProfile/internal/repository"
	"github.com/kkapel/GophProfile/internal/storage"
	"github.com/kkapel/GophProfile/internal/tracing"
	"github.com/kkapel/GophProfile/internal/worker"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
)

func main() {
	if err := run(); err != nil {
		slog.Error("worker stopped with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadConfig()
	if err != nil {
		return err
	}

	// Общие метаданные телеметрии: один ресурс
	// дальше передаётся и в логгер, и в трассировщик.
	ctx := context.Background()
	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			semconv.ServiceNameKey.String("gophprofile-worker"),
			semconv.ServiceVersionKey.String("1.0.0"),
		),
	)
	if err != nil {
		return fmt.Errorf("create resource: %w", err)
	}

	log, shutdownLogger, err := logger.New(context.Background(), res, "gophprofile-worker", "1.0.0", cfg.LoggerLevel)
	if err != nil {
		return err
	}

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := shutdownLogger(shutdownCtx); err != nil {
			slog.Error("shutdown logger", "err", err)
		}
	}()

	log = log.With("component", "worker")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Миграции накатывает сервер, worker только подключается.
	db, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("init database: %w", err)
	}
	defer db.Close()

	store, err := storage.New(ctx, storage.Config{
		Endpoint:  cfg.MinioEndpoint,
		AccessKey: cfg.MinioAccessKey,
		SecretKey: cfg.MinioSecretKey,
		Bucket:    cfg.MinioBucket,
		UseSSL:    cfg.MinioUseSSL,
	})
	if err != nil {
		return fmt.Errorf("init storage: %w", err)
	}

	rabbit, err := broker.NewRabbitMQ(cfg.RabbitMQURL)
	if err != nil {
		return fmt.Errorf("init broker: %w", err)
	}
	defer func() {
		_ = rabbit.Close()
	}()

	appMetrics := metrics.New()
	// Отдельный HTTP-сервер только для метрик
	metricsSrv := &http.Server{
		Addr:              cfg.MetricsAddress,
		Handler:           appMetrics.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.InfoContext(ctx, "metrics server started", "addr", cfg.MetricsAddress)
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.ErrorContext(ctx, "metrics server error", "err", err)
		}
	}()

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
			log.ErrorContext(context.Background(), "shutdown metrics server", "err", err)
		}
	}()

	// Трейсинг
	shutdownTracing, err := tracing.Init(ctx, res, "gophprofile-worker", "1.0.0", cfg.TraceSampleRatio)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := shutdownTracing(shutdownCtx); err != nil {
			log.ErrorContext(context.Background(), "shutdown tracing", "err", err)
		}
	}()

	avatarRepo := repository.NewAvatarRepository(db.Pool)
	w := worker.New(avatarRepo, store, rabbit, log, appMetrics)

	log.InfoContext(ctx, "worker started")

	if err := w.Run(ctx); err != nil {
		return fmt.Errorf("worker run: %w", err)
	}

	log.InfoContext(ctx, "worker stopped")

	return nil
}
