// Package logger инициализирует и предоставляет структурированный логгер приложения.
package logger

import (
	"log/slog"
	"os"
)

// Log — глобальный логгер приложения. По умолчанию пишет JSON уровня Info в stdout.
var Log *slog.Logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

// Initialize настраивает глобальный логгер на заданный уровень (например, "info", "debug", "error").
func Initialize(level string) error {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return err
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})
	Log = slog.New(handler)
	return nil
}
