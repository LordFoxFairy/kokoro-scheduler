package system

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"time"
)

type Sleeper struct{}

func (Sleeper) Wait(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type CryptoRandomSource struct{}

func (CryptoRandomSource) Int63n(maxExclusive int64) (int64, error) {
	if maxExclusive <= 0 {
		return 0, errors.New("random upper bound must be positive")
	}
	value, err := rand.Int(rand.Reader, big.NewInt(maxExclusive))
	if err != nil {
		return 0, err
	}
	return value.Int64(), nil
}
