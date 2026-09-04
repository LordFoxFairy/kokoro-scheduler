package application

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/ports"
)

type Runtime struct {
	wakeup    ports.Wakeup
	processor *Processor
	observer  ports.ErrorObserver
	ctx       context.Context
	cancel    context.CancelFunc
	ready     atomic.Bool

	mu        sync.Mutex
	accepting bool
	inFlight  sync.WaitGroup
}

func NewRuntime(wakeup ports.Wakeup, processor *Processor, observer ports.ErrorObserver) (*Runtime, error) {
	if wakeup == nil || processor == nil {
		return nil, ErrInvalidDependencies
	}
	if observer == nil {
		observer = ports.NoopErrorObserver{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Runtime{wakeup: wakeup, processor: processor, observer: observer, ctx: ctx, cancel: cancel}, nil
}

func (r *Runtime) Start() error {
	r.mu.Lock()
	if r.accepting {
		r.mu.Unlock()
		return nil
	}
	r.accepting = true
	r.mu.Unlock()
	if err := r.wakeup.Start(func(context.Context) { r.trigger() }); err != nil {
		r.mu.Lock()
		r.accepting = false
		r.mu.Unlock()
		return err
	}
	r.ready.Store(true)
	r.trigger()
	return nil
}

func (r *Runtime) trigger() {
	r.mu.Lock()
	if !r.accepting {
		r.mu.Unlock()
		return
	}
	r.inFlight.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.inFlight.Done()
		if err := r.processor.RunCycle(r.ctx); err != nil {
			r.observer.ObserveError("scheduler_cycle", err)
		}
	}()
}

func (r *Runtime) Ready() bool { return r.ready.Load() }

func (r *Runtime) Stop(ctx context.Context) error {
	r.ready.Store(false)
	r.mu.Lock()
	r.accepting = false
	r.mu.Unlock()
	r.cancel()
	wakeupErr := r.wakeup.Stop(ctx)
	done := make(chan struct{})
	go func() {
		r.inFlight.Wait()
		close(done)
	}()
	select {
	case <-done:
		return wakeupErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
