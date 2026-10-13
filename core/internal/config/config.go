// Package config loads Grantra settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// Config holds the runtime settings for the core server.
type Config struct {
	HTTPAddr        string
	DatabaseURL     string
	ShutdownTimeout time.Duration
	LogLevel        string
}

// FromEnv reads the configuration from environment variables, applying defaults.
func FromEnv() (Config, error) {
	cfg := Config{
		HTTPAddr:        getenv("GRANTRA_HTTP_ADDR", ":8080"),
		DatabaseURL:     os.Getenv("GRANTRA_DATABASE_URL"),
		ShutdownTimeout: 15 * time.Second,
		LogLevel:        getenv("GRANTRA_LOG_LEVEL", "info"),
	}
	if v := os.Getenv("GRANTRA_SHUTDOWN_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("GRANTRA_SHUTDOWN_TIMEOUT: %w", err)
		}
		cfg.ShutdownTimeout = d
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("GRANTRA_DATABASE_URL is required")
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
