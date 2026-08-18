// Команда migrate применяет миграции базы данных и завершается.
// Запускается как Helm-хук перед установкой и обновлением релиза.
package main

import (
	"log/slog"
	"os"

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

	if err = database.RunMigrations(cfg.DatabaseURL, cfg.MigrationsPath); err != nil {
		return err
	}

	slog.Info("migrations applied")

	return nil
}
