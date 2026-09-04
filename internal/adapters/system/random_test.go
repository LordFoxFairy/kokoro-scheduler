package system

import (
	"testing"
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
