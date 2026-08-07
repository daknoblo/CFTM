package notify

import (
	"context"
	"log/slog"
	"time"

	"github.com/daknoblo/CFTM/internal/collector"
	"github.com/daknoblo/CFTM/internal/store"
)

// Config controls which conditions produce a notification.
type Config struct {
	Enabled bool
	// MinSeverity drops anything less urgent, "warning" by default.
	MinSeverity collector.Severity
	// Cooldown is how long the same condition stays quiet after being reported.
	Cooldown time.Duration
}

// Dispatcher turns events into notifications, suppresses repeats and records
// every decision in the outbox.
type Dispatcher struct {
	store    *store.Store
	notifier Notifier
	log      *slog.Logger
	cfg      Config
}

// New returns a Dispatcher. A nil notifier means notifications are evaluated
// and recorded but not delivered.
func New(st *store.Store, notifier Notifier, cfg Config, log *slog.Logger) *Dispatcher {
	if notifier == nil {
		notifier = Discard{}
	}
	if cfg.MinSeverity == "" {
		cfg.MinSeverity = collector.SeverityWarning
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = time.Hour
	}
	return &Dispatcher{store: st, notifier: notifier, log: log, cfg: cfg}
}

// Transport names the configured delivery channel.
func (d *Dispatcher) Transport() string { return d.notifier.Name() }

// Dispatch evaluates a batch of events. It never returns an error: a failed
// notification must not abort a collection cycle.
func (d *Dispatcher) Dispatch(ctx context.Context, events []store.Event, tunnelNames map[string]string) {
	if !d.cfg.Enabled || len(events) == 0 {
		return
	}

	for _, e := range events {
		n, ok := FromEvent(e, tunnelNames[e.TunnelID])
		if !ok || !d.urgentEnough(n) {
			continue
		}

		recent, err := d.store.NotifiedSince(ctx, n.Key, n.Timestamp.Add(-d.cfg.Cooldown))
		if err != nil {
			d.log.Warn("checking the notification cooldown failed", "key", n.Key, "err", err)
			continue
		}
		if recent {
			continue
		}

		d.deliver(ctx, n)
	}
}

func (d *Dispatcher) urgentEnough(n Notification) bool {
	return collector.Severity(n.Severity).Rank() >= d.cfg.MinSeverity.Rank()
}

func (d *Dispatcher) deliver(ctx context.Context, n Notification) {
	record := store.Notification{
		Key:       n.Key,
		CreatedAt: n.Timestamp,
		Severity:  n.Severity,
		Kind:      n.Kind,
		TunnelID:  n.TunnelID,
		Hostname:  n.Hostname,
		Title:     n.Title,
		Body:      n.Body,
		Transport: d.notifier.Name(),
		Status:    store.NotificationSent,
	}

	if err := d.notifier.Send(ctx, n); err != nil {
		record.Status = store.NotificationFailed
		record.Error = err.Error()
		d.log.Warn("sending notification failed", "title", n.Title, "err", err)
	}

	// The outbox is written either way: a failure that leaves no trace is worse
	// than one that does, and it is what feeds the cooldown.
	if err := d.store.AddNotification(ctx, record); err != nil {
		d.log.Error("recording notification failed", "title", n.Title, "err", err)
	}
}
