// Package config загружает конфигурацию сервиса из переменных окружения.
package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

// Config содержит параметры запуска сервиса GophProfile.
type Config struct {
	HTTPAddress    string
	LoggerLevel    string
	MigrationsPath string
	MetricsAddress string

	TraceSampleRatio float64

	DatabaseURL string

	MinioEndpoint  string
	MinioAccessKey string
	MinioSecretKey string
	MinioBucket    string
	MinioUseSSL    bool

	RabbitMQURL string
}

// LoadConfig читает конфигурацию из переменных окружения (префикс GOPHPROFILE_)
// и проверяет наличие обязательных параметров.
func LoadConfig() (*Config, error) {
	v := viper.New()

	// Дефолтные (несекретные) значения.
	v.SetDefault("http_address", ":8080")
	v.SetDefault("logger_level", "INFO")
	v.SetDefault("minio_endpoint", "localhost:9000")
	v.SetDefault("minio_bucket", "avatars")
	v.SetDefault("minio_use_ssl", false)
	v.SetDefault("migrations_path", "migrations")
	v.SetDefault("metrics_address", ":8081")
	v.SetDefault("trace_sample_ratio", 1.0)

	// Переменные окружения: GOPHPROFILE_DATABASE_URL и т.д.
	v.AllowEmptyEnv(true)
	v.SetEnvPrefix("GOPHPROFILE")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	config := &Config{
		HTTPAddress:    v.GetString("http_address"),
		LoggerLevel:    v.GetString("logger_level"),
		MigrationsPath: v.GetString("migrations_path"),
		MetricsAddress: v.GetString("metrics_address"),

		TraceSampleRatio: v.GetFloat64("trace_sample_ratio"),

		DatabaseURL: v.GetString("database_url"),

		MinioEndpoint:  v.GetString("minio_endpoint"),
		MinioAccessKey: v.GetString("minio_access_key"),
		MinioSecretKey: v.GetString("minio_secret_key"),
		MinioBucket:    v.GetString("minio_bucket"),
		MinioUseSSL:    v.GetBool("minio_use_ssl"),

		RabbitMQURL: v.GetString("rabbitmq_url"),
	}

	if err := config.validate(); err != nil {
		return nil, err
	}
	return config, nil
}

// validate проверяет, что заданы обязательные секреты и параметры.
func (c *Config) validate() error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	if c.MinioAccessKey == "" {
		return fmt.Errorf("MINIO_ACCESS_KEY is required")
	}
	if c.MinioSecretKey == "" {
		return fmt.Errorf("MINIO_SECRET_KEY is required")
	}
	if c.RabbitMQURL == "" {
		return fmt.Errorf("RABBITMQ_URL is required")
	}
	if c.TraceSampleRatio < 0 || c.TraceSampleRatio > 1 {
		return fmt.Errorf("TRACE_SAMPLE_RATIO must be between 0 and 1, got %v", c.TraceSampleRatio)
	}

	return nil
}
