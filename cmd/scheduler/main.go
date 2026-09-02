package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	scheduler "github.com/LordFoxFairy/kokoro-scheduler"
	"github.com/redis/go-redis/v9"
)

func main() {
	jobs, err := scheduler.LoadJobs(os.Getenv("SCHEDULER_JOBS_JSON"))
	if err != nil {
		log.Fatal(err)
	}
	var redisClient *redis.Client
	var locker scheduler.Locker
	if rawRedisURL := os.Getenv("SCHEDULER_REDIS_URL"); rawRedisURL != "" {
		options, err := redis.ParseURL(rawRedisURL)
		if err != nil {
			log.Fatalf("parse SCHEDULER_REDIS_URL: %v", err)
		}
		redisClient = redis.NewClient(options)
		if err := redisClient.Ping(context.Background()).Err(); err != nil {
			log.Fatalf("scheduler Redis coordination is unavailable: %v", err)
		}
		locker = scheduler.NewRedisLocker(redisClient)
		defer redisClient.Close()
	}
	service := scheduler.NewService(scheduler.NewHTTPRunner(30*time.Second), func(job scheduler.Job, result scheduler.RunResult) {
		log.Printf("scheduler job=%s status=%d error=%v", job.Name, result.Status, result.Err)
	}, locker)
	for _, job := range jobs {
		if err := service.Add(job); err != nil {
			log.Fatalf("add job %q: %v", job.Name, err)
		}
	}
	service.Start()

	httpAddr := strings.TrimSpace(os.Getenv("SCHEDULER_HTTP_ADDR"))
	if httpAddr == "" {
		httpAddr = ":8080"
	}
	httpServer := &http.Server{
		Addr:              httpAddr,
		Handler:           scheduler.NewHTTPHandler(service, os.Getenv("SCHEDULER_INTERNAL_SERVICE_TOKEN")),
		ReadHeaderTimeout: 5 * time.Second,
	}
	httpErrors := make(chan error, 1)
	go func() {
		log.Printf("scheduler HTTP server listening addr=%s", httpAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			httpErrors <- err
		}
	}()
	log.Printf("scheduler started jobs=%d", len(jobs))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case <-ctx.Done():
	case err := <-httpErrors:
		log.Printf("scheduler HTTP server failed: %v", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("scheduler HTTP shutdown: %v", err)
	}
	shutdown := service.Stop(shutdownCtx)
	<-shutdown.Done()
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"stopped": true})
}
