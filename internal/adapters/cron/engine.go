package cronadapter

import (
	"context"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/ports"
	"github.com/robfig/cron/v3"
)

type Engine struct {
	cron *cron.Cron
}

func NewEngine() *Engine {
	return &Engine{cron: cron.New(
		cron.WithLocation(time.UTC),
		cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger)),
	)}
}

func (e *Engine) Add(spec string, run func()) (ports.EntryID, error) {
	entry, err := e.cron.AddFunc(spec, run)
	return ports.EntryID(entry), err
}

func (e *Engine) Remove(id ports.EntryID) {
	e.cron.Remove(cron.EntryID(id))
}

func (e *Engine) Start() { e.cron.Start() }

func (e *Engine) Stop(ctx context.Context) error {
	done := e.cron.Stop()
	select {
	case <-done.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
