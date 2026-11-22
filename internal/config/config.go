// Package config reads and validates process configuration.
package config

import (
	"errors"
	"log/slog"
	"net"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr         string
	DatabaseURL      string
	DBStartupTimeout time.Duration
	LogLevel         slog.Level
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:         value("HTTP_ADDR", ":8080"),
		DatabaseURL:      os.Getenv("DATABASE_URL"),
		DBStartupTimeout: 30 * time.Second,
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	_, port, err := net.SplitHostPort(cfg.HTTPAddr)
	if err != nil {
		return Config{}, errors.New("HTTP_ADDR must be a host:port address")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return Config{}, errors.New("HTTP_ADDR port must be between 1 and 65535")
	}
	cfg.DBStartupTimeout, err = time.ParseDuration(value("DB_STARTUP_TIMEOUT", "30s"))
	if err != nil || cfg.DBStartupTimeout <= 0 {
		return Config{}, errors.New("DB_STARTUP_TIMEOUT must be a positive duration")
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(value("LOG_LEVEL", "info"))); err != nil {
		return Config{}, errors.New("LOG_LEVEL must be a valid slog level")
	}
	return cfg, nil
}

func value(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
