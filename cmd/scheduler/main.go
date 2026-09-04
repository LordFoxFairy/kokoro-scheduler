package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	gocronadapter "github.com/LordFoxFairy/kokoro-scheduler/internal/adapters/gocron"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/adapters/httpclient"
	postgresadapter "github.com/LordFoxFairy/kokoro-scheduler/internal/adapters/postgres"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/adapters/recurrence"
	redisadapter "github.com/LordFoxFairy/kokoro-scheduler/internal/adapters/redis"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/adapters/system"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/application"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/config"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/ports"
	transporthttp "github.com/LordFoxFairy/kokoro-scheduler/internal/transport/http"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type logObserver struct{ logger *slog.Logger }

func (o logObserver) Observe(work domain.DispatchWork, result domain.DispatchResult) {
	outcome := "succeeded"
	if !result.Succeeded() {
		outcome = "failed"
	}
	args := []any{
		"operation", "dispatch",
		"tenant_id", work.TenantID,
		"schedule", work.ScheduleName,
		"result", outcome,
		"status", result.Status,
		"attempt", work.AttemptCount,
		"code", result.Code,
		"request_id", result.RequestID,
		"trace_id", result.TraceID,
		"duration_ms", result.Duration.Milliseconds(),
	}
	if result.Err != nil {
		o.logger.Error("scheduler dispatch failed", append(args, "error", result.Err.Error())...)
		return
	}
	o.logger.Info("scheduler dispatch completed", args...)
}

type logErrorObserver struct{ logger *slog.Logger }

func (o logErrorObserver) ObserveError(operation string, err error) {
	o.logger.Error("scheduler background operation failed", "operation", operation, "error", err.Error())
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		os.Exit(runHealthcheck())
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})).With(
		"service", "kokoro-scheduler",
	)
	if err := run(logger); err != nil {
		logger.Error("scheduler stopped with an error", "error", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("load scheduler configuration: %w", err)
	}
	startupCtx, startupCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer startupCancel()
	pool, err := pgxpool.New(startupCtx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open scheduler PostgreSQL store: %w", err)
	}
	defer pool.Close()
	store := postgresadapter.NewStore(pool)
	if err := store.Ping(startupCtx); err != nil {
		return fmt.Errorf("ping scheduler PostgreSQL store: %w", err)
	}

	redisClient, leaseStore, err := openRedis(startupCtx, cfg.RedisURL)
	if err != nil {
		return err
	}
	if redisClient != nil {
		defer redisClient.Close()
	}

	workerID, err := resolveWorkerID(cfg.WorkerID)
	if err != nil {
		return err
	}
	calculator := recurrence.NewCalculator()
	service, err := application.NewService(store, ports.SystemClock{}, calculator)
	if err != nil {
		return err
	}
	planner, err := application.NewPlanner(store, ports.SystemClock{}, calculator, workerID, cfg.ClaimTTL, cfg.BatchSize)
	if err != nil {
		return err
	}
	var targetAllowlist httpclient.AddressAllowlist
	if cfg.InternalTargetAllowlist != nil {
		targetAllowlist = cfg.InternalTargetAllowlist
	}
	dispatcher, err := application.NewDispatcher(application.DispatcherDependencies{
		Store: store, Clock: ports.SystemClock{}, Random: system.CryptoRandomSource{},
		Target:     httpclient.NewDefaultClientWithAllowlist(cfg.DispatchTimeout, cfg.TargetServiceToken, targetAllowlist),
		LeaseStore: leaseStore, Observer: logObserver{logger: logger}, WorkerID: workerID,
		ClaimTTL: cfg.ClaimTTL, DispatchTimeout: cfg.DispatchTimeout, BatchSize: cfg.BatchSize,
	})
	if err != nil {
		return err
	}
	processor, err := application.NewProcessor(planner, dispatcher)
	if err != nil {
		return err
	}
	wakeup, err := gocronadapter.NewWakeup(cfg.WakeupInterval)
	if err != nil {
		return err
	}
	runtime, err := application.NewRuntime(wakeup, processor, logErrorObserver{logger: logger})
	if err != nil {
		return err
	}
	if err := runtime.Start(); err != nil {
		return fmt.Errorf("start scheduler wakeup adapter: %w", err)
	}

	server := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: transporthttp.NewHTTPHandler(service, cfg.InternalServiceToken, func(ctx context.Context) error {
			if !runtime.Ready() {
				return errors.New("scheduler runtime is not accepting work")
			}
			if err := store.Ping(ctx); err != nil {
				return err
			}
			if redisClient != nil {
				return redisClient.Ping(ctx).Err()
			}
			return nil
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       35 * time.Second,
		WriteTimeout:      35 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("scheduler HTTP server listening", "addr", cfg.HTTPAddr, "worker_id", workerID)
		if serveErr := server.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			serverErrors <- serveErr
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-serverErrors:
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	serverErr := server.Shutdown(shutdownCtx)
	runtimeErr := runtime.Stop(shutdownCtx)
	return errors.Join(serveErr, serverErr, runtimeErr)
}

func openRedis(ctx context.Context, rawURL string) (*redis.Client, ports.LeaseStore, error) {
	if strings.TrimSpace(rawURL) == "" {
		return nil, nil, nil
	}
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, nil, fmt.Errorf("parse scheduler Redis URL: %w", err)
	}
	if options.DB != 7 {
		return nil, nil, errors.New("scheduler Redis URL must select logical DB 7")
	}
	client := redis.NewClient(options)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("ping scheduler Redis coordination: %w", err)
	}
	return client, redisadapter.NewStore(client, 2*time.Second), nil
}

func resolveWorkerID(configured string) (string, error) {
	if configured = strings.TrimSpace(configured); configured != "" {
		return configured, nil
	}
	hostname, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("resolve scheduler worker hostname: %w", err)
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("generate scheduler worker identity: %w", err)
	}
	return hostname + "-" + hex.EncodeToString(suffix[:]), nil
}

func runHealthcheck() int {
	url := os.Getenv("SCHEDULER_HEALTHCHECK_URL")
	if url == "" {
		url = "http://127.0.0.1:8080/readyz"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		return 1
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
