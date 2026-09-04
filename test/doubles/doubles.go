package doubles

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/ports"
)

type Clock struct {
	mu      sync.Mutex
	Current time.Time
}

func NewClock(current time.Time) *Clock { return &Clock{Current: current} }

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Current
}

func (c *Clock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.Current = c.Current.Add(duration)
	c.mu.Unlock()
}

type RandomSource struct {
	mu     sync.Mutex
	Values []int64
	Limits []int64
	Err    error
}

func (s *RandomSource) Int63n(maxExclusive int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if maxExclusive <= 0 {
		return 0, errors.New("random upper bound must be positive")
	}
	s.Limits = append(s.Limits, maxExclusive)
	if s.Err != nil {
		return 0, s.Err
	}
	if len(s.Values) == 0 {
		return 0, nil
	}
	value := s.Values[0]
	s.Values = s.Values[1:]
	if value < 0 || value >= maxExclusive {
		return 0, fmt.Errorf("random value %d outside [0,%d)", value, maxExclusive)
	}
	return value, nil
}

type TargetClient struct {
	mu      sync.Mutex
	Calls   []domain.DispatchWork
	Results []domain.DispatchResult
	Block   <-chan struct{}
}

func (c *TargetClient) Dispatch(ctx context.Context, work domain.DispatchWork) domain.DispatchResult {
	c.mu.Lock()
	c.Calls = append(c.Calls, work)
	c.mu.Unlock()
	if c.Block != nil {
		select {
		case <-c.Block:
		case <-ctx.Done():
			return domain.DispatchResult{Err: ctx.Err(), Code: domain.CodeTargetTimeout}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.Results) == 0 {
		return domain.DispatchResult{Status: 202}
	}
	result := c.Results[0]
	c.Results = c.Results[1:]
	return result
}

func (c *TargetClient) CallCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.Calls)
}

type Wakeup struct {
	mu      sync.Mutex
	run     func(context.Context)
	Started bool
	Stopped bool
}

func (w *Wakeup) Start(run func(context.Context)) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.run = run
	w.Started = true
	return nil
}

func (w *Wakeup) Stop(context.Context) error {
	w.mu.Lock()
	w.Stopped = true
	w.mu.Unlock()
	return nil
}

func (w *Wakeup) Trigger() {
	w.mu.Lock()
	run := w.run
	w.mu.Unlock()
	if run != nil {
		run(context.Background())
	}
}

type LeaseStore struct {
	mu     sync.Mutex
	claims map[string]struct{}
}

func NewLeaseStore() *LeaseStore { return &LeaseStore{claims: make(map[string]struct{})} }

func (s *LeaseStore) Acquire(_ context.Context, key string, _ time.Duration) (ports.Lease, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.claims[key]; exists {
		return nil, false, nil
	}
	s.claims[key] = struct{}{}
	return &lease{store: s, key: key}, true, nil
}

type lease struct {
	store *LeaseStore
	key   string
}

func (*lease) Renew(context.Context, time.Duration) error { return nil }

func (l *lease) Release(context.Context) error {
	l.store.mu.Lock()
	delete(l.store.claims, l.key)
	l.store.mu.Unlock()
	return nil
}
