package config

import (
	"testing"
	"time"
)

func configEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"HTTP_ADDR", "DATABASE_URL", "DB_STARTUP_TIMEOUT", "LOG_LEVEL"} {
		t.Setenv(key, "")
	}
	t.Setenv("DATABASE_URL", "postgres://ino:local@postgres:5432/ino?sslmode=disable")
}

func TestLoadDefaults(t *testing.T) {
	configEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.DBStartupTimeout != 30*time.Second {
		t.Fatalf("defaults = %+v", cfg)
	}
}

func TestLoadOverrides(t *testing.T) {
	configEnv(t)
	t.Setenv("HTTP_ADDR", "127.0.0.1:9000")
	t.Setenv("DB_STARTUP_TIMEOUT", "5s")
	t.Setenv("LOG_LEVEL", "debug")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != "127.0.0.1:9000" || cfg.DBStartupTimeout != 5*time.Second || cfg.LogLevel.String() != "DEBUG" {
		t.Fatalf("overrides not applied")
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"DATABASE_URL", ""}, {"HTTP_ADDR", "localhost"}, {"HTTP_ADDR", ":70000"},
		{"DB_STARTUP_TIMEOUT", "invalid"}, {"DB_STARTUP_TIMEOUT", "0s"},
		{"DB_STARTUP_TIMEOUT", "-1s"}, {"LOG_LEVEL", "invalid"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			configEnv(t)
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
