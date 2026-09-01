package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/signal"
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
	log.Printf("scheduler started jobs=%d", len(jobs))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	shutdown := service.Stop(ctx)
	<-shutdown.Done()
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"stopped": true})
}
