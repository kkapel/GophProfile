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
	"github.com/kkapel/GophProfile/internal/metrics"
	"github.com/kkapel/GophProfile/internal/repository"
	"github.com/kkapel/GophProfile/internal/services"
	"github.com/kkapel/GophProfile/internal/storage"
	"github.com/kkapel/GophProfile/internal/tracing"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
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

	ctx := context.Background()

	// Общие метаданные телеметрии: один ресурс
	// дальше передаётся и в логгер, и в трассировщик.
	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			semconv.ServiceNameKey.String("gophprofile-server"),
			semconv.ServiceVersionKey.String("1.0.0"),
		),
	)

	if err != nil {
		return fmt.Errorf("create resource: %w", err)
	}

	// Логгер
	log, shutdownLogger, err := logger.New(ctx, res, "gophprofile-server", "1.0.0", cfg.LoggerLevel)
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

	log = log.With("component", "server")
	log.InfoContext(ctx, "logger initialized")

	// Подключение к PostgreSQL и применение миграций
	db, err := database.New(ctx, cfg.DatabaseURL, cfg.MigrationsPath)
	if err != nil {
		return fmt.Errorf("init database: %w", err)
	}
	defer db.Close()
	log.InfoContext(ctx, "database connected, migrations applied")

	// Статистика пула
	appMetrics := metrics.New()
	poolGauges := []struct {
		name  string
		help  string
		value func() float64
	}{
		{"gophprofile_db_connections_total", "Total number of connections in the pool",
			func() float64 { return float64(db.Pool.Stat().TotalConns()) }},
		{"gophprofile_db_connections_acquired", "Number of currently acquired connections",
			func() float64 { return float64(db.Pool.Stat().AcquiredConns()) }},
		{"gophprofile_db_connections_idle", "Number of idle connections",
			func() float64 { return float64(db.Pool.Stat().IdleConns()) }},
	}

	for _, g := range poolGauges {
		if err := appMetrics.RegisterGaugeFunc(g.name, g.help, nil, g.value); err != nil {
			return err
		}
	}

	// Трейсинг
	shutdownTracing, err := tracing.Init(ctx, res, "gophprofile-server", "1.0.0", cfg.TraceSampleRatio)
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
	log.InfoContext(ctx, "storage connected", "bucket", cfg.MinioBucket)

	// Подключение к брокеру сообщений
	rabbit, err := broker.NewRabbitMQ(cfg.RabbitMQURL)
	if err != nil {
		return fmt.Errorf("init broker: %w", err)
	}
	defer func() {
		_ = rabbit.Close()
	}()
	log.InfoContext(ctx, "broker connected")

	// Сборка слоёв приложения
	avatarRepo := repository.NewAvatarRepository(db.Pool)
	avatarService := services.NewAvatarService(avatarRepo, store, rabbit, log.With("layer", "service"), appMetrics)
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
	r.Use(otelhttp.NewMiddleware("gophprofile-server"))
	r.Use(handlers.LoggingMiddleware(log.With("layer", "http")))
	r.Use(handlers.MetricsMiddleware(appMetrics))
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

	r.Handle("/metrics", appMetrics.Handler())

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
		log.InfoContext(ctx, "server started", "addr", cfg.HTTPAddress)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.ErrorContext(ctx, "server error", "err", err)
			srvErr <- err
		}
	}()

	collector := metrics.NewCollector(appMetrics, rabbit, avatarRepo,
		[]string{broker.QueueProcess, broker.QueueDelete}, log.With("layer", "metrics"))

	go collector.Run(ctx, 15*time.Second)

	// Ожидаем SIGINT/SIGTERM.
	signalCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-srvErr:
		return fmt.Errorf("listen and serve: %w", err)
	case <-signalCtx.Done():
	}

	log.InfoContext(ctx, "shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	log.InfoContext(ctx, "stopped")

	return nil
}
