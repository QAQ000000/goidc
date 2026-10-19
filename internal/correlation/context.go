// Package correlation carries opaque diagnostic origins. These identifiers do
// not authorize a request and are distinct from business idempotency keys.
package correlation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
)

type contextKey struct{}

// Valid restricts identifiers to a fixed, log-safe representation.
func Valid(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func NewID() string {
	var raw [16]byte
	// crypto/rand.Read terminates on an unavailable secure random source.
	_, _ = rand.Read(raw[:])
	return hex.EncodeToString(raw[:])
}

func ID(ctx context.Context) string {
	id, _ := ctx.Value(contextKey{}).(string)
	return id
}

// WithID clears a parent's origin when id is absent or invalid. In particular,
// historical queue jobs must not inherit an unrelated polling request's ID.
func WithID(ctx context.Context, id string) context.Context {
	if !Valid(id) {
		id = ""
	}
	return context.WithValue(ctx, contextKey{}, id)
}

// JobContext restores a durable origin. Historical jobs without one get a new
// attempt identifier, without inventing or changing the stored historical origin.
func JobContext(ctx context.Context, origin string) context.Context {
	if !Valid(origin) {
		origin = NewID()
	}
	return WithID(ctx, origin)
}

func Logger(ctx context.Context, log *slog.Logger) *slog.Logger {
	if id := ID(ctx); id != "" {
		return log.With("correlation_id", id)
	}
	return log
}

type originError struct {
	err error
	id  string
}

func (e originError) Error() string { return e.err.Error() }
func (e originError) Unwrap() error { return e.err }

// WrapError returns diagnostic metadata to a caller whose context is outside a
// restored job scope. The error text and errors.Is/As behavior remain unchanged.
func WrapError(ctx context.Context, err error) error {
	if err == nil || ID(ctx) == "" {
		return err
	}
	var existing originError
	if errors.As(err, &existing) {
		return err
	}
	return originError{err: err, id: ID(ctx)}
}

func FromError(ctx context.Context, err error) context.Context {
	var origin originError
	if errors.As(err, &origin) && Valid(origin.id) {
		return WithID(ctx, origin.id)
	}
	return ctx
}

// Handler lets shared loggers attach the current context without mutating the
// logger. Callers must use the context-aware slog methods to supply that context.
func Handler(next slog.Handler) slog.Handler {
	return contextHandler{next: next}
}

type contextHandler struct{ next slog.Handler }

func (h contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h contextHandler) Handle(ctx context.Context, record slog.Record) error {
	if id := ID(ctx); id != "" {
		record.AddAttrs(slog.String("correlation_id", id))
	}
	return h.next.Handle(ctx, record)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{next: h.next.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{next: h.next.WithGroup(name)}
}
