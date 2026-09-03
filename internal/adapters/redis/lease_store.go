package redisadapter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/ports"
	"github.com/redis/go-redis/v9"
)

const defaultOperationTimeout = 2 * time.Second

var (
	releaseScript = redis.NewScript(`if redis.call('get', KEYS[1]) == ARGV[1] then return redis.call('del', KEYS[1]) else return 0 end`)
	renewScript   = redis.NewScript(`if redis.call('get', KEYS[1]) == ARGV[1] then return redis.call('pexpire', KEYS[1], ARGV[2]) else return 0 end`)
)

type Store struct {
	client           *redis.Client
	operationTimeout time.Duration
}

func NewStore(client *redis.Client, operationTimeout time.Duration) *Store {
	if operationTimeout <= 0 {
		operationTimeout = defaultOperationTimeout
	}
	return &Store{client: client, operationTimeout: operationTimeout}
}

func (s *Store) Acquire(ctx context.Context, key string, ttl time.Duration) (ports.Lease, bool, error) {
	if s == nil || s.client == nil {
		return nil, false, errors.New("redis lease store is not configured")
	}
	if key == "" || ttl <= 0 {
		return nil, false, errors.New("lease key and ttl are required")
	}
	token, err := randomToken()
	if err != nil {
		return nil, false, err
	}
	operationCtx, cancel := context.WithTimeout(ctx, s.operationTimeout)
	defer cancel()
	acquired, err := s.client.SetNX(operationCtx, key, token, ttl).Result()
	if err != nil || !acquired {
		return nil, acquired, err
	}
	return &lease{client: s.client, key: key, token: token, operationTimeout: s.operationTimeout}, true, nil
}

type lease struct {
	client           *redis.Client
	key              string
	token            string
	operationTimeout time.Duration
}

func (l *lease) Renew(ctx context.Context, ttl time.Duration) error {
	if ttl <= 0 {
		return errors.New("lease ttl must be positive")
	}
	operationCtx, cancel := context.WithTimeout(ctx, l.operationTimeout)
	defer cancel()
	result, err := renewScript.Run(operationCtx, l.client, []string{l.key}, l.token, ttl.Milliseconds()).Int()
	if err != nil {
		return err
	}
	if result != 1 {
		return errors.New("lease is no longer owned")
	}
	return nil
}

func (l *lease) Release(ctx context.Context) error {
	operationCtx, cancel := context.WithTimeout(ctx, l.operationTimeout)
	defer cancel()
	_, err := releaseScript.Run(operationCtx, l.client, []string{l.key}, l.token).Result()
	return err
}

func randomToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
