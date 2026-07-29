// Команда worker обрабатывает события из брокера сообщений:
// создаёт миниатюры аватарок и удаляет файлы из хранилища.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/kkapel/GophProfile/internal/broker"
	"github.com/kkapel/GophProfile/internal/config"
	"github.com/kkapel/GophProfile/internal/database"
	"github.com/kkapel/GophProfile/internal/logger"
	"github.com/kkapel/GophProfile/internal/repository"
	"github.com/kkapel/GophProfile/internal/storage"
	"github.com/kkapel/GophProfile/internal/worker"
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

	log, err := logger.New(cfg.LoggerLevel)
	if err != nil {
		return err
	}
	log = log.With("component", "worker")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Миграции накатывает сервер, worker только подключается.
	db, err := database.New(ctx, cfg.DatabaseURL, cfg.MigrationsPath)
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

	avatarRepo := repository.NewAvatarRepository(db.Pool)
	w := worker.New(avatarRepo, store, rabbit, log)

	log.Info("worker started")

	if err := w.Run(ctx); err != nil {
		return fmt.Errorf("worker run: %w", err)
	}

	log.Info("worker stopped")

	return nil
}
