// Command seed populates a fresh INO database with synthetic assessment data.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ino/internal/adapter/postgres"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Getenv("DATABASE_URL"))
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, databaseURL string) error {
	if databaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	pool, err := postgres.OpenWithMaxConns(ctx, databaseURL, 1)
	if err != nil {
		return err
	}
	defer pool.Close()

	inserted, err := postgres.SeedAssessment(ctx, pool)
	if err != nil {
		return err
	}
	if inserted {
		fmt.Println("Assessment seed inserted and verified")
	} else {
		fmt.Println("Assessment seed skipped: database already contains application data")
	}
	return nil
}
