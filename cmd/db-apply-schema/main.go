package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	postgresadapter "github.com/LordFoxFairy/kokoro-scheduler/internal/adapters/postgres"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	databaseURL := strings.TrimSpace(os.Getenv(config.DatabaseURLEnv))
	if databaseURL == "" {
		return fmt.Errorf("db:apply-schema requires %s", config.DatabaseURLEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("open PostgreSQL target: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping PostgreSQL target: %w", err)
	}
	if err := postgresadapter.ApplySchemaToEmptyDatabase(ctx, pool); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(os.Stdout, "db:apply-schema installed database/schema.sql")
	return nil
}
