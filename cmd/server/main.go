// Команда server поднимает HTTP API сервиса аватарок.
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

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/kkapel/GophProfile/internal/api"
	"github.com/kkapel/GophProfile/internal/broker"
	"github.com/kkapel/GophProfile/internal/config"
	"github.com/kkapel/GophProfile/internal/database"
	"github.com/kkapel/GophProfile/internal/handlers"
	"github.com/kkapel/GophProfile/internal/logger"
	"github.com/kkapel/GophProfile/internal/repository"
	"github.com/kkapel/GophProfile/internal/services"
	"github.com/kkapel/GophProfile/internal/storage"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped with error", "error", err)
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
	log, err := logger.New(cfg.LoggerLevel)
	if err != nil {
		return err
	}
	log = log.With("component", "server")
	log.Info("logger initialized")

	ctx := context.Background()

	// Подключение к PostgreSQL и применение миграций
	db, err := database.New(ctx, cfg.DatabaseURL, cfg.MigrationsPath)
	if err != nil {
		return fmt.Errorf("init database: %w", err)
	}
	defer db.Close()
	log.Info("database connected, migrations applied")

	// Подключение к объектному хранилищу
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
	log.Info("storage connected", "bucket", cfg.MinioBucket)

	// Подключение к брокеру сообщений
	rabbit, err := broker.NewRabbitMQ(cfg.RabbitMQURL)
	if err != nil {
		return fmt.Errorf("init broker: %w", err)
	}
	defer func() {
		_ = rabbit.Close()
	}()
	log.Info("broker connected")

	// Сборка слоёв приложения
	avatarRepo := repository.NewAvatarRepository(db.Pool)
	avatarService := services.NewAvatarService(avatarRepo, store, rabbit)
	avatarHandler := handlers.NewAvatarHandler(avatarService, log)

	webHandler, err := handlers.NewWebHandler(avatarService, log)
	if err != nil {
		return fmt.Errorf("init web handler: %w", err)
	}

	healthHandler := handlers.NewHealthHandler(map[string]handlers.Pinger{
		"database": db,
		"storage":  store,
		"broker":   rabbit,
	})

	r := chi.NewRouter()
	// Добавить хэндлеры
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)

	// Веб-интерфейс
	r.Get("/web/upload", webHandler.UploadPage)
	r.Post("/web/upload", webHandler.Upload)
	r.Get("/web/gallery/{user_id}", webHandler.GalleryPage)
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/web/upload", http.StatusFound)
	})

	// Делаем пинг для дб и объектного хранилища
	r.Method(http.MethodGet, "/health", healthHandler)

	// Регистрация путей REST API из OpenAPI-спецификации
	api.HandlerFromMuxWithBaseURL(avatarHandler, r, "/api/v1")

	srv := &http.Server{
		Addr:         cfg.HTTPAddress,
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	// Запуск в отдельной горутине, чтобы не блокировать ожидание сигнала.
	srvErr := make(chan error, 1)
	go func() {
		log.Info("server started", "addr", cfg.HTTPAddress)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", "err", err)
			srvErr <- err
		}
	}()

	// Ожидаем SIGINT/SIGTERM.
	signalCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-srvErr:
		return fmt.Errorf("listen and serve: %w", err)
	case <-signalCtx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	log.Info("stopped")

	return nil
}
