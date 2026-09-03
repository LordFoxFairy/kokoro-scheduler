package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/adapters/cron"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/adapters/httpclient"
	redisadapter "github.com/LordFoxFairy/kokoro-scheduler/internal/adapters/redis"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/adapters/system"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/application"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/config"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/ports"
	transporthttp "github.com/LordFoxFairy/kokoro-scheduler/internal/transport/http"
	"github.com/redis/go-redis/v9"
)

type logObserver struct{ logger *slog.Logger }

func (o logObserver) Observe(job domain.Job, result domain.RunResult) {
	outcome := "succeeded"
	if !result.Succeeded() {
		outcome = "failed"
	}
	args := []any{
		"operation", "dispatch",
		"job", job.Name,
		"result", outcome,
		"status", result.Status,
		"attempts", result.Attempts,
		"code", result.Code,
		"request_id", result.RequestID,
		"trace_id", result.TraceID,
		"duration_ms", result.Duration.Milliseconds(),
	}
	if result.Err != nil {
		args = append(args, "error", result.Err.Error())
		o.logger.Error("scheduler dispatch failed", args...)
		return
	}
	o.logger.Info("scheduler dispatch completed", args...)
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		os.Exit(runHealthcheck())
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})).With(
		"service", "kokoro-scheduler",
	)
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		logger.Error("scheduler startup failed", "error", err.Error())
		os.Exit(1)
	}

	var redisClient *redis.Client
	var leaseStore ports.LeaseStore
	if cfg.RedisURL != "" {
		options, parseErr := redis.ParseURL(cfg.RedisURL)
		if parseErr != nil {
			logger.Error("parse scheduler redis url failed", "env", config.RedisURLEnv, "error", parseErr.Error())
			os.Exit(1)
		}
		redisClient = redis.NewClient(options)
		pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		pingErr := redisClient.Ping(pingCtx).Err()
		cancel()
		if pingErr != nil {
			_ = redisClient.Close()
			logger.Error("scheduler Redis coordination is unavailable", "error", pingErr.Error())
			os.Exit(1)
		}
		leaseStore = redisadapter.NewStore(redisClient, 2*time.Second)
		defer redisClient.Close()
	}

	scheduler, err := application.NewScheduler(application.Dependencies{
		Engine:          cronadapter.NewEngine(),
		Clock:           ports.SystemClock{},
		Sleeper:         system.Sleeper{},
		Random:          system.CryptoRandomSource{},
		LeaseStore:      leaseStore,
		TargetClient:    httpclient.NewDefaultClient(cfg.DispatchTimeout, cfg.TargetServiceToken),
		Observer:        logObserver{logger: logger},
		DispatchTimeout: cfg.DispatchTimeout,
		LeaseTTL:        cfg.LeaseTTL,
	})
	if err != nil {
		logger.Error("scheduler startup failed", "error", err.Error())
		os.Exit(1)
	}
	for _, job := range cfg.Jobs {
		if err := scheduler.Register(job); err != nil {
			logger.Error("register scheduler job failed", "job", job.Name, "error", err.Error())
			os.Exit(1)
		}
	}

	server := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: transporthttp.NewHTTPHandler(scheduler, cfg.InternalServiceToken, func(ctx context.Context) error {
			if !scheduler.Ready() {
				return errors.New("scheduler is not running")
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
		logger.Info("scheduler HTTP server listening", "addr", cfg.HTTPAddr)
		if serveErr := server.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			serverErrors <- serveErr
		}
	}()
	scheduler.Start()
	logger.Info("scheduler started", "jobs", len(cfg.Jobs))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case <-ctx.Done():
	case serveErr := <-serverErrors:
		logger.Error("scheduler HTTP server failed", "error", serveErr.Error())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("scheduler HTTP shutdown failed", "error", err.Error())
	}
	if err := scheduler.Stop(shutdownCtx); err != nil {
		logger.Error("scheduler shutdown failed", "error", err.Error())
	}
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
