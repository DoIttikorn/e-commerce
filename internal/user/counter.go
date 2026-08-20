package user

import (
	"context"
	"log/slog"
	"time"
)

// counter is the slice of the service the logger needs.
type counter interface {
	Count(ctx context.Context) (int64, error)
}

// CountLogger periodically reports how many users exist.
//
// It is an operational helper rather than a business rule, but it lives with
// the domain it counts so that adding a domain never means editing main.
type CountLogger struct {
	svc      counter
	log      *slog.Logger
	interval time.Duration
}

// NewCountLogger returns a logger that reports every interval.
func NewCountLogger(svc counter, log *slog.Logger, interval time.Duration) *CountLogger {
	return &CountLogger{svc: svc, log: log, interval: interval}
}

// Run blocks until ctx is cancelled, so the caller decides whether it owns a
// goroutine. It returns promptly on cancellation rather than finishing the
// current wait, which is what keeps it out of the shutdown path.
//
// A Ticker rather than repeated Sleep: sleeping adds the duration of the count
// to every cycle, so the schedule drifts further behind the longer it runs.
func (c *CountLogger) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.report(ctx)
		}
	}
}

func (c *CountLogger) report(ctx context.Context) {
	total, err := c.svc.Count(ctx)
	if err != nil {
		// Logged, not returned: a failed count is worth knowing about but is
		// no reason to stop reporting, and there is nobody to return it to.
		c.log.LogAttrs(ctx, slog.LevelError, "user count failed",
			slog.String("error", err.Error()))
		return
	}

	c.log.LogAttrs(ctx, slog.LevelInfo, "user count", slog.Int64("total", total))
}
