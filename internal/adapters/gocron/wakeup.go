package gocronadapter

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
)

type Wakeup struct {
	scheduler gocron.Scheduler
	interval  time.Duration

	mu      sync.Mutex
	started bool
}

func NewWakeup(interval time.Duration) (*Wakeup, error) {
	if interval <= 0 {
		return nil, errors.New("gocron wakeup interval must be positive")
	}
	scheduler, err := gocron.NewScheduler(gocron.WithLocation(time.UTC))
	if err != nil {
		return nil, err
	}
	return &Wakeup{scheduler: scheduler, interval: interval}, nil
}

func (w *Wakeup) Start(run func(context.Context)) error {
	if run == nil {
		return errors.New("gocron wakeup callback is required")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return nil
	}
	if _, err := w.scheduler.NewJob(
		gocron.DurationJob(w.interval),
		gocron.NewTask(run),
	); err != nil {
		return err
	}
	w.scheduler.Start()
	w.started = true
	return nil
}

func (w *Wakeup) Stop(ctx context.Context) error {
	w.mu.Lock()
	started := w.started
	w.started = false
	w.mu.Unlock()
	if !started {
		return nil
	}
	return w.scheduler.ShutdownWithContext(ctx)
}
