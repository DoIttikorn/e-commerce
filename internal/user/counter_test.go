package user

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

type fakeCounter struct {
	calls atomic.Int32
	err   error
}

func (f *fakeCounter) Count(context.Context) (int64, error) {
	f.calls.Add(1)
	return 42, f.err
}

func TestCountLoggerReportsOnEveryTick(t *testing.T) {
	fake := &fakeCounter{}
	logger := NewCountLogger(fake, slog.New(slog.DiscardHandler), time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { logger.Run(ctx); close(done) }()

	deadline := time.After(2 * time.Second)
	for fake.calls.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("only %d ticks in 2s, want at least 3", fake.calls.Load())
		default:
			time.Sleep(time.Millisecond)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return promptly after cancellation")
	}
}

// A failing count must not stop the loop: the next tick should try again.
func TestCountLoggerSurvivesErrors(t *testing.T) {
	fake := &fakeCounter{err: errors.New("mongo unavailable")}
	logger := NewCountLogger(fake, slog.New(slog.DiscardHandler), time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logger.Run(ctx)

	deadline := time.After(2 * time.Second)
	for fake.calls.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("the loop stopped after the first error")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

// Cancelling before the first tick must return immediately rather than waiting
// out the interval, or shutdown is held up by however long the interval is.
func TestCountLoggerReturnsImmediatelyWhenAlreadyCancelled(t *testing.T) {
	logger := NewCountLogger(&fakeCounter{}, slog.New(slog.DiscardHandler), time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() { logger.Run(ctx); close(done) }()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run blocked on a cancelled context")
	}
}
