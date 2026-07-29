// Package logger создаёт логгеры приложения.
package logger

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// New создаёт JSON-логгер, пишущий в stdout с указанным уровнем.
// Допустимые уровни: DEBUG, INFO, WARN, ERROR.
func New(level string) (*slog.Logger, error) {
	parsed, err := parseLevel(level)
	if err != nil {
		return nil, err
	}

	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parsed})), nil
}

// parseLevel переводит строковый уровень логирования в slog.Level.
func parseLevel(level string) (slog.Level, error) {
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "DEBUG":
		return slog.LevelDebug, nil
	case "INFO":
		return slog.LevelInfo, nil
	case "WARN":
		return slog.LevelWarn, nil
	case "ERROR":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown logger level %q", level)
	}
}
