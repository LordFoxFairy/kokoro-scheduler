package system

import (
	"crypto/rand"
	"errors"
	"math/big"
)

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
