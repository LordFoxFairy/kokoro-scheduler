package system

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCryptoRandomSourceReturnsValueInsideRequestedRange(t *testing.T) {
	const upperBound = int64(17)
	for i := 0; i < 64; i++ {
		value, err := (CryptoRandomSource{}).Int63n(upperBound)
		if err != nil {
			t.Fatal(err)
		}
		if value < 0 || value >= upperBound {
			t.Fatalf("random value = %d, want [0,%d)", value, upperBound)
		}
	}
}

func TestCryptoRandomSourceRejectsNonPositiveUpperBound(t *testing.T) {
	if _, err := (CryptoRandomSource{}).Int63n(0); err == nil {
		t.Fatal("non-positive upper bound must fail")
	}
}

func TestSleeperHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	err := (Sleeper{}).Wait(ctx, time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cancelled wait took %s", elapsed)
	}
}
