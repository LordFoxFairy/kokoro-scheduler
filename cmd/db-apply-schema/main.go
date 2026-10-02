package main

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	target, err := config.ParseDatabaseURL(os.Getenv(config.DatabaseURLEnv))
	if err != nil {
		return fmt.Errorf("load %s: %w", config.DatabaseURLEnv, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, target.DriverURL())
	if err != nil {
		return errors.New("open PostgreSQL target failed")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return errors.New("ping PostgreSQL target failed")
	}
	if err := postgresadapter.ApplySchemaToEmptyDatabase(ctx, pool, target); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(os.Stdout, "db:apply-schema installed database/schema.sql")
	return nil
}
