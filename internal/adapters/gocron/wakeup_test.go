package gocronadapter

import (
	"context"
	"testing"
	"time"
)

func TestWakeupOnlyInvokesProcessCallback(t *testing.T) {
	wakeup, err := NewWakeup(10 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	called := make(chan struct{}, 1)
	if err := wakeup.Start(func(context.Context) {
		select {
		case called <- struct{}{}:
		default:
		}
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("gocron wakeup did not call the process scanner")
	}
	if err := wakeup.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWakeupRejectsNonPositiveInterval(t *testing.T) {
	if _, err := NewWakeup(0); err == nil {
		t.Fatal("non-positive wakeup interval must fail")
	}
}
