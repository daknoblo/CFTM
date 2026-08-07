package notify

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/daknoblo/CFTM/internal/collector"
	"github.com/daknoblo/CFTM/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "cftm.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// recorder captures what a transport would have received.
type recorder struct {
	sent []Notification
	err  error
}

func (r *recorder) Name() string { return "recorder" }

func (r *recorder) Send(_ context.Context, n Notification) error {
	r.sent = append(r.sent, n)
	return r.err
}

func newDispatcher(t *testing.T, st *store.Store, rec Notifier, cfg Config) *Dispatcher {
	t.Helper()
	cfg.Enabled = true
	return New(st, rec, cfg, slog.New(slog.DiscardHandler))
}

func TestNoisyEventKindsAreNeverNotified(t *testing.T) {
	for _, kind := range []string{
		store.EventConfigChanged, store.EventConnectorAdded, store.EventTunnelAdded,
	} {
		if _, ok := FromEvent(store.Event{Kind: kind}, "edge"); ok {
			t.Errorf("%s should not produce a notification", kind)
		}
	}
}

func TestSeverityThresholdIsHonored(t *testing.T) {
	st := newStore(t)
	rec := &recorder{}
	d := newDispatcher(t, st, rec, Config{MinSeverity: collector.SeverityCritical})

	now := time.Now()
	d.Dispatch(t.Context(), []store.Event{
		// warning: below the threshold
		{Kind: store.EventConnectorRemoved, TunnelID: "t1", Timestamp: now, Message: "connector gone"},
		// critical: above it
		{Kind: store.EventTunnelStatus, TunnelID: "t1", ToState: "down", Timestamp: now, Message: "tunnel down"},
	}, map[string]string{"t1": "edge"})

	if len(rec.sent) != 1 {
		t.Fatalf("sent %d notifications, want 1", len(rec.sent))
	}
	if got, want := rec.sent[0].Title, "edge is down"; got != want {
		t.Errorf("Title = %q, want %q", got, want)
	}
}

func TestTheSameConditionIsOnlyReportedOncePerCooldown(t *testing.T) {
	st := newStore(t)
	rec := &recorder{}
	d := newDispatcher(t, st, rec, Config{MinSeverity: collector.SeverityWarning, Cooldown: time.Hour})

	now := time.Now()
	down := store.Event{Kind: store.EventTunnelStatus, TunnelID: "t1", ToState: "down", Timestamp: now, Message: "down"}
	names := map[string]string{"t1": "edge"}

	d.Dispatch(t.Context(), []store.Event{down}, names)
	// Same condition ten minutes later: still inside the cooldown.
	repeat := down
	repeat.Timestamp = now.Add(10 * time.Minute)
	d.Dispatch(t.Context(), []store.Event{repeat}, names)

	if len(rec.sent) != 1 {
		t.Fatalf("sent %d notifications, want 1 while inside the cooldown", len(rec.sent))
	}

	// Recovery is a different target state, so it must get through immediately.
	up := store.Event{Kind: store.EventTunnelStatus, TunnelID: "t1", ToState: "healthy", Timestamp: now.Add(20 * time.Minute)}
	d2 := newDispatcher(t, st, rec, Config{MinSeverity: collector.SeverityInfo, Cooldown: time.Hour})
	d2.Dispatch(t.Context(), []store.Event{up}, names)

	if len(rec.sent) != 2 {
		t.Fatalf("sent %d notifications, want the recovery to get through", len(rec.sent))
	}
}

func TestAFailedDeliveryIsStillRecorded(t *testing.T) {
	st := newStore(t)
	rec := &recorder{err: errors.New("transport unreachable")}
	d := newDispatcher(t, st, rec, Config{MinSeverity: collector.SeverityWarning})

	d.Dispatch(t.Context(), []store.Event{
		{Kind: store.EventPollFailed, Timestamp: time.Now(), Message: "boom"},
	}, nil)

	out, err := st.Notifications(t.Context(), 10)
	if err != nil {
		t.Fatalf("Notifications() error = %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("outbox has %d entries, want 1", len(out))
	}
	if out[0].Status != store.NotificationFailed {
		t.Errorf("Status = %q, want %q", out[0].Status, store.NotificationFailed)
	}
	if out[0].Error == "" {
		t.Error("a failed delivery must record why")
	}
}

func TestDisabledDispatcherDoesNothing(t *testing.T) {
	st := newStore(t)
	rec := &recorder{}
	d := New(st, rec, Config{Enabled: false}, slog.New(slog.DiscardHandler))

	d.Dispatch(t.Context(), []store.Event{
		{Kind: store.EventPollFailed, Timestamp: time.Now()},
	}, nil)

	if len(rec.sent) != 0 {
		t.Errorf("sent %d notifications while disabled, want 0", len(rec.sent))
	}
	out, err := st.Notifications(t.Context(), 10)
	if err != nil {
		t.Fatalf("Notifications() error = %v", err)
	}
	if len(out) != 0 {
		t.Errorf("outbox has %d entries while disabled, want 0", len(out))
	}
}

func TestDiscardIsTheDefaultTransport(t *testing.T) {
	d := New(newStore(t), nil, Config{Enabled: true}, slog.New(slog.DiscardHandler))
	if got, want := d.Transport(), "none"; got != want {
		t.Errorf("Transport() = %q, want %q", got, want)
	}
}
