// Package database устанавливает подключение к PostgreSQL
// и применяет миграции.
package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // драйвер миграций
	_ "github.com/golang-migrate/migrate/v4/source/file"       // источник миграций
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB — обёртка над пулом подключений к PostgreSQL.
type DB struct {
	Pool *pgxpool.Pool
}

// New применяет миграции и открывает пул подключений к базе данных.
func New(ctx context.Context, dsn, migrationsPath string) (*DB, error) {
	if dsn == "" {
		return nil, errors.New("database DSN is empty")
	}

	if err := runMigrations(dsn, migrationsPath); err != nil {
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.MaxConns = 25
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 5 * time.Minute
	cfg.MaxConnIdleTime = time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	db := &DB{Pool: pool}
	if err := db.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	return db, nil
}

// Ping проверяет доступность базы данных.
func (db *DB) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return db.Pool.Ping(ctx)
}

// Close закрывает пул подключений.
func (db *DB) Close() {
	db.Pool.Close()
}

// runMigrations накатывает миграции из указанной директории.
func runMigrations(dsn, migrationsPath string) error {
	m, err := migrate.New("file://"+migrationsPath, dsn)
	if err != nil {
		return fmt.Errorf("init migrate: %w", err)
	}
	defer func() {
		_, _ = m.Close()
	}()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}

	return nil
}
