// Команда migrate применяет миграции базы данных и завершается.
// Запускается как Helm-хук перед установкой и обновлением релиза.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/kkapel/GophProfile/internal/config"
	"github.com/kkapel/GophProfile/internal/database"
)

func main() {
	if err := run(); err != nil {
		slog.Error("migration failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadConfig()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// database.New применяет миграции и открывает пул подключений.
	db, err := database.New(ctx, cfg.DatabaseURL, cfg.MigrationsPath)
	if err != nil {
		return err
	}
	defer db.Close()

	slog.Info("migrations applied")

	return nil
}
