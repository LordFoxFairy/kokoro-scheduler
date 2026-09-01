package scheduler

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestRedisLockerProvidesOneOccurrenceClaim(t *testing.T) {
	rawURL := os.Getenv("SCHEDULER_REDIS_TEST_URL")
	if rawURL == "" {
		t.Skip("SCHEDULER_REDIS_TEST_URL is not configured")
	}
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	defer client.Close()
	locker := NewRedisLocker(client)
	key := "kokoro:test:scheduler:" + time.Now().UTC().Format("20060102150405.000000000")
	release, acquired, err := locker.Acquire(context.Background(), key, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("first claim: acquired=%v err=%v", acquired, err)
	}
	defer release()
	_, acquired, err = locker.Acquire(context.Background(), key, time.Minute)
	if err != nil || acquired {
		t.Fatalf("second claim: acquired=%v err=%v", acquired, err)
	}
	release()
	_, acquired, err = locker.Acquire(context.Background(), key, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("claim after release: acquired=%v err=%v", acquired, err)
	}
}
