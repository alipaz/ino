// Command migrate applies the embedded Goose migrations to PostgreSQL.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"ino/migrations"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Getenv("DATABASE_URL"))
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, databaseURL string) error {
	if len(args) != 1 || (args[0] != "up" && args[0] != "status") {
		return errors.New("usage: migrate up|status")
	}
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	cfg, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return errors.New("invalid DATABASE_URL")
	}
	db := stdlib.OpenDB(*cfg)
	defer db.Close()

	sessionLocker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("configure migration lock: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.Files, goose.WithSessionLocker(sessionLocker))
	if err != nil {
		return fmt.Errorf("configure Goose: %w", err)
	}

	if args[0] == "status" {
		statuses, err := provider.Status(ctx)
		if err != nil {
			return fmt.Errorf("read migration status: %w", err)
		}
		for _, status := range statuses {
			fmt.Printf("%s %s\n", status.State, status.Source.Path)
		}
		return nil
	}
	applied, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	for _, migration := range applied {
		fmt.Printf("applied %s\n", migration.Source.Path)
	}
	if len(applied) == 0 {
		fmt.Println("No pending migrations")
	}
	return nil
}
