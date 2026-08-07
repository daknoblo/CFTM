// Package notify decides which events are worth telling someone about and
// records the outcome. The transport is deliberately pluggable: everything here
// works, and is worth having, before a delivery channel is chosen.
package notify

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/daknoblo/CFTM/internal/collector"
	"github.com/daknoblo/CFTM/internal/store"
)

// Notification is one message about to leave the system.
type Notification struct {
	// Key identifies the underlying condition rather than the single event, so
	// a flapping tunnel does not produce one message per poll.
	Key        string    `json:"key"`
	Timestamp  time.Time `json:"timestamp"`
	Severity   string    `json:"severity"`
	Kind       string    `json:"kind"`
	Title      string    `json:"title"`
	Body       string    `json:"body"`
	TunnelID   string    `json:"tunnelId,omitempty"`
	TunnelName string    `json:"tunnelName,omitempty"`
	Hostname   string    `json:"hostname,omitempty"`
}

// Notifier delivers a notification. Implementations must be safe for concurrent
// use and must not block longer than the context allows.
type Notifier interface {
	// Name identifies the transport in logs and on the About page.
	Name() string
	Send(ctx context.Context, n Notification) error
}

// Discard is the default transport: notifications are still evaluated,
// deduplicated and recorded, they are simply not delivered anywhere yet.
type Discard struct{}

// Name implements Notifier.
func (Discard) Name() string { return "none" }

// Send implements Notifier.
func (Discard) Send(context.Context, Notification) error { return nil }

// FromEvent turns an event into a notification. The second return value is
// false for events that are never worth waking someone for.
func FromEvent(e store.Event, tunnelName string) (Notification, bool) {
	switch e.Kind {
	// Configuration and inventory changes are expected during normal work and
	// would train the reader to ignore the channel.
	case store.EventConfigChanged, store.EventConnectorAdded, store.EventTunnelAdded:
		return Notification{}, false
	}

	severity := collector.EventSeverity(e)
	n := Notification{
		Key:        dedupKey(e),
		Timestamp:  e.Timestamp,
		Severity:   string(severity),
		Kind:       e.Kind,
		Title:      title(e, tunnelName),
		Body:       e.Message,
		TunnelID:   e.TunnelID,
		TunnelName: tunnelName,
		Hostname:   e.Hostname,
	}
	return n, true
}

// dedupKey collapses repeats of the same condition. The target state is part of
// the key so a recovery is still announced after an outage.
func dedupKey(e store.Event) string {
	parts := []string{e.Kind, e.TunnelID, e.Hostname, e.ToState}
	return strings.Join(parts, "|")
}

func title(e store.Event, tunnelName string) string {
	subject := tunnelName
	if subject == "" {
		subject = e.Hostname
	}
	if subject == "" {
		subject = "CFTM"
	}

	switch e.Kind {
	case store.EventTunnelStatus:
		return fmt.Sprintf("%s is %s", subject, e.ToState)
	case store.EventTunnelRemoved:
		return fmt.Sprintf("Tunnel %s disappeared", subject)
	case store.EventConnectorRemoved:
		return fmt.Sprintf("%s lost a connector", subject)
	case store.EventProbe:
		return fmt.Sprintf("%s probe: %s", subject, e.ToState)
	case store.EventPollFailed:
		return "Cloudflare poll failed"
	case store.EventVersionDrift:
		return fmt.Sprintf("%s runs an outdated cloudflared", subject)
	case store.EventServiceTokenAging:
		return "An Access service token is expiring"
	case store.EventAccessCoverage:
		return fmt.Sprintf("%s is publicly reachable", subject)
	default:
		return fmt.Sprintf("%s: %s", subject, collector.EventLabel(e.Kind))
	}
}
