// Package logging builds the application logger and carries the request ID
// that ties together every line written while serving one request.
package logging

import (
	"context"
	"io"
	"log/slog"
)

type requestIDKey struct{}

// WithRequestID returns a context carrying id.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFrom returns the request ID carried by ctx, or "" if there is none.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// New returns a JSON logger that stamps every record with the request ID from
// the context it was logged with.
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(&contextHandler{
		Handler: slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}),
	})
}

// contextHandler copies request-scoped values onto every record.
//
// Correlating in the handler rather than at each call site means any code that
// logs with the request context is correlated for free, including code deep in
// a domain that knows nothing about HTTP.
type contextHandler struct {
	slog.Handler
}

func (h *contextHandler) Handle(ctx context.Context, rec slog.Record) error {
	if id := RequestIDFrom(ctx); id != "" {
		rec.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, rec)
}

// WithAttrs and WithGroup must rewrap: the embedded handler's versions return
// the inner handler, which would silently drop the correlation from any logger
// derived with slog.With.

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithGroup(name)}
}
