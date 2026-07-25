// Команда worker обрабатывает события из брокера сообщений:
// создаёт миниатюры аватарок и удаляет файлы из хранилища.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/kkapel/GophProfile/internal/config"
	"github.com/kkapel/GophProfile/internal/logger"
)

func main() {
	if err := run(); err != nil {
		slog.Error("worker stopped with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	// Инициализация конфигурации
	cfg, err := config.LoadConfig()
	if err != nil {
		return err
	}

	// Логгер
	if err := logger.Initialize(cfg.LoggerLevel); err != nil {
		return err
	}
	logger.Log.Info("worker started")

	// TODO(этап 6): подключение к RabbitMQ, PostgreSQL и MinIO,
	// подписка на очереди и обработка событий.

	// Ожидаем SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	logger.Log.Info("worker stopped")

	return nil
}
