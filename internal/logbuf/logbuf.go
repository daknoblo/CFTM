// Package logbuf provides an in-memory ring buffer of recent log records that
// can be displayed in the UI, in addition to normal slog output to stdout.
package logbuf

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Entry is a single captured log record.
type Entry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

// Buffer is a fixed-capacity ring buffer of log entries, safe for concurrent use.
type Buffer struct {
	mu      sync.RWMutex
	entries []Entry
	start   int // index of the oldest entry
	count   int // number of entries currently held
	max     int
}

// New returns a Buffer holding at most max entries.
func New(max int) *Buffer {
	if max <= 0 {
		max = 200
	}
	return &Buffer{max: max, entries: make([]Entry, max)}
}

// Add appends an entry, evicting the oldest when at capacity. Insertion is O(1)
// so a burst of debug logging during a poll does not shift the whole buffer.
func (b *Buffer) Add(e Entry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries[(b.start+b.count)%b.max] = e
	if b.count < b.max {
		b.count++
		return
	}
	b.start = (b.start + 1) % b.max
}

// Entries returns a snapshot of the buffered entries, newest last.
func (b *Buffer) Entries() []Entry {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Entry, b.count)
	n := copy(out, b.entries[b.start:min(b.start+b.count, b.max)])
	copy(out[n:], b.entries[:b.count-n])
	return out
}

// Handler is an slog.Handler that records messages into a Buffer while
// delegating formatting and output to a wrapped handler.
type Handler struct {
	inner slog.Handler
	buf   *Buffer
}

// NewHandler wraps inner so that every record is also captured in buf.
func NewHandler(inner slog.Handler, buf *Buffer) *Handler {
	return &Handler{inner: inner, buf: buf}
}

// Enabled implements slog.Handler.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle implements slog.Handler.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	h.buf.Add(Entry{
		Time:    r.Time,
		Level:   r.Level.String(),
		Message: r.Message,
	})
	return h.inner.Handle(ctx, r)
}

// WithAttrs implements slog.Handler.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{inner: h.inner.WithAttrs(attrs), buf: h.buf}
}

// WithGroup implements slog.Handler.
func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{inner: h.inner.WithGroup(name), buf: h.buf}
}
