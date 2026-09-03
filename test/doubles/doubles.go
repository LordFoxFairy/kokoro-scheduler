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

type Sleeper struct {
	mu        sync.Mutex
	Clock     *Clock
	Durations []time.Duration
	Err       error
}

func (s *Sleeper) Wait(ctx context.Context, duration time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return s.Err
	}
	s.Durations = append(s.Durations, duration)
	if s.Clock != nil {
		s.Clock.Advance(duration)
	}
	return nil
}

func (s *Sleeper) Waits() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.Durations...)
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

func (s *RandomSource) UpperBounds() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.Limits...)
}

type ScheduleEngine struct {
	mu      sync.Mutex
	next    int
	entries map[int]func()
	started bool
	stopped bool
}

func NewScheduleEngine() *ScheduleEngine { return &ScheduleEngine{entries: make(map[int]func())} }

func (e *ScheduleEngine) Add(_ string, run func()) (ports.EntryID, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.next++
	e.entries[e.next] = run
	return ports.EntryID(e.next), nil
}
func (e *ScheduleEngine) Remove(id ports.EntryID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.entries, int(id))
}
func (e *ScheduleEngine) Start() { e.mu.Lock(); e.started = true; e.mu.Unlock() }
func (e *ScheduleEngine) Stop(context.Context) error {
	e.mu.Lock()
	e.stopped = true
	e.mu.Unlock()
	return nil
}
func (e *ScheduleEngine) Trigger(id int) {
	e.mu.Lock()
	run := e.entries[id]
	e.mu.Unlock()
	if run != nil {
		run()
	}
}

// TargetClient is a deterministic test double and never ships in production.
type TargetClient struct {
	mu      sync.Mutex
	Calls   int
	Results []domain.RunResult
	Block   <-chan struct{}
}

func (c *TargetClient) Dispatch(ctx context.Context, _ domain.Job, _ domain.Occurrence) domain.RunResult {
	c.mu.Lock()
	c.Calls++
	c.mu.Unlock()
	if c.Block != nil {
		select {
		case <-c.Block:
		case <-ctx.Done():
			return domain.RunResult{Err: ctx.Err(), Code: "SCHEDULER_TARGET_TIMEOUT"}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.Results) == 0 {
		return domain.RunResult{Status: 202}
	}
	result := c.Results[0]
	c.Results = c.Results[1:]
	return result
}

func (c *TargetClient) CallCount() int { c.mu.Lock(); defer c.mu.Unlock(); return c.Calls }

type LeaseStore struct {
	mu           sync.Mutex
	claims       map[string]bool
	AcquireCalls int
	RenewCalls   int
}

func NewLeaseStore() *LeaseStore { return &LeaseStore{claims: make(map[string]bool)} }

func (s *LeaseStore) Acquire(_ context.Context, key string, _ time.Duration) (ports.Lease, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.AcquireCalls++
	if s.claims[key] {
		return nil, false, nil
	}
	s.claims[key] = true
	return &lease{store: s}, true, nil
}

type lease struct{ store *LeaseStore }

func (l *lease) Renew(context.Context, time.Duration) error {
	l.store.mu.Lock()
	l.store.RenewCalls++
	l.store.mu.Unlock()
	return nil
}
func (l *lease) Release(context.Context) error { return nil }

func (s *LeaseStore) RenewalCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.RenewCalls
}
