package redisadapter

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestStoreClaimsAndRenewsOnlyItsOwnLease(t *testing.T) {
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
	store := NewStore(client, time.Second)
	key := "kokoro:test:scheduler:lease:" + time.Now().UTC().Format("20060102150405.000000000")
	lease, acquired, err := store.Acquire(context.Background(), key, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("first acquire acquired=%v err=%v", acquired, err)
	}
	defer lease.Release(context.Background())
	if _, acquired, err := store.Acquire(context.Background(), key, time.Minute); err != nil || acquired {
		t.Fatalf("second acquire acquired=%v err=%v", acquired, err)
	}
	if err := lease.Renew(context.Background(), time.Minute); err != nil {
		t.Fatalf("renew: %v", err)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, acquired, err := store.Acquire(context.Background(), key, time.Minute); err != nil || !acquired {
		t.Fatalf("acquire after release acquired=%v err=%v", acquired, err)
	}
}
