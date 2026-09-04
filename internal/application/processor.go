package application

import (
	"context"
	"errors"
)

type Processor struct {
	planner    *Planner
	dispatcher *Dispatcher
}

func NewProcessor(planner *Planner, dispatcher *Dispatcher) (*Processor, error) {
	if planner == nil || dispatcher == nil {
		return nil, ErrInvalidDependencies
	}
	return &Processor{planner: planner, dispatcher: dispatcher}, nil
}

func (p *Processor) RunCycle(ctx context.Context) error {
	planErr := p.planner.Run(ctx)
	dispatchErr := p.dispatcher.Run(ctx)
	return errors.Join(planErr, dispatchErr)
}
