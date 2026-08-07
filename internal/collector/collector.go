// Package collector polls the Cloudflare API, persists snapshots and derives
// health findings.
package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/daknoblo/CFTM/internal/cloudflare"
	"github.com/daknoblo/CFTM/internal/store"
)

// Config controls the polling cadence and retention.
type Config struct {
	PollInterval       time.Duration
	ConfigRefreshEvery int
	RetentionDays      int
}

// Status is the in-memory view of the collector's own health.
type Status struct {
	LastRun     time.Time            `json:"lastRun"`
	LastSuccess time.Time            `json:"lastSuccess"`
	LastError   string               `json:"lastError"`
	DurationMS  int                  `json:"durationMs"`
	RateLimit   cloudflare.RateLimit `json:"rateLimit"`
	Cycles      int                  `json:"cycles"`
}

// EventSink receives every batch of events the collector records. It exists so
// notifications can be attached without the collector knowing what a
// notification is.
type EventSink interface {
	Dispatch(ctx context.Context, events []store.Event, tunnelNames map[string]string)
}

// Collector keeps the local database in sync with the Cloudflare API.
type Collector struct {
	cf    *cloudflare.Client
	store *store.Store
	log   *slog.Logger
	cfg   Config
	sink  EventSink

	mu     sync.RWMutex
	status Status

	trigger chan struct{}
}

// WithEventSink attaches an observer for recorded events.
func (c *Collector) WithEventSink(sink EventSink) *Collector {
	c.sink = sink
	return c
}

// New returns a Collector.
func New(cf *cloudflare.Client, st *store.Store, cfg Config, log *slog.Logger) *Collector {
	if cfg.ConfigRefreshEvery <= 0 {
		cfg.ConfigRefreshEvery = 10
	}
	return &Collector{
		cf:      cf,
		store:   st,
		log:     log,
		cfg:     cfg,
		trigger: make(chan struct{}, 1),
	}
}

// Status returns a snapshot of the collector's own health.
func (c *Collector) Status() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	status := c.status
	status.RateLimit = c.cf.RateLimit()
	return status
}

// Trigger requests an out-of-band poll without blocking the caller.
func (c *Collector) Trigger() {
	select {
	case c.trigger <- struct{}{}:
	default:
	}
}

// Run polls until the context is canceled.
func (c *Collector) Run(ctx context.Context) {
	c.PollOnce(ctx)

	ticker := time.NewTicker(c.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.trigger:
		case <-ticker.C:
			// Spread load so a restart storm does not align every request.
			if err := jitter(ctx, c.cfg.PollInterval); err != nil {
				return
			}
		}
		c.PollOnce(ctx)
	}
}

// PollOnce runs a single collection cycle and records its outcome.
func (c *Collector) PollOnce(ctx context.Context) {
	started := time.Now()
	err := c.poll(ctx, started)
	elapsed := int(time.Since(started).Milliseconds())

	c.mu.Lock()
	c.status.LastRun = started
	c.status.DurationMS = elapsed
	c.status.Cycles++
	cycle := c.status.Cycles
	if err == nil {
		c.status.LastSuccess = started
		c.status.LastError = ""
	} else {
		c.status.LastError = err.Error()
	}
	c.mu.Unlock()

	run := store.PollRun{StartedAt: started, DurationMS: elapsed, OK: err == nil}
	if err != nil {
		run.Error = err.Error()
		c.log.Warn("poll failed", "err", err, "cycle", cycle)
		c.appendEvents(ctx, []store.Event{{
			Timestamp: started,
			Kind:      store.EventPollFailed,
			Message:   err.Error(),
		}})
	}
	if err := c.store.AddPollRun(ctx, run); err != nil {
		c.log.Error("recording poll run failed", "err", err)
	}

	if cycle%pruneEveryCycles == 0 {
		c.prune(ctx, started)
	}
}

// pruneEveryCycles keeps retention cheap without a second scheduler.
const pruneEveryCycles = 120

func (c *Collector) poll(ctx context.Context, now time.Time) error {
	apiTunnels, err := c.cf.ListTunnels(ctx)
	if err != nil {
		return fmt.Errorf("list tunnels: %w", err)
	}

	tunnels := make([]store.Tunnel, 0, len(apiTunnels))
	for _, t := range apiTunnels {
		tunnels = append(tunnels, toStoreTunnel(t))
	}

	diff, err := c.store.SaveTunnels(ctx, tunnels, now)
	if err != nil {
		return fmt.Errorf("save tunnels: %w", err)
	}
	events := tunnelEvents(diff, now)

	c.mu.RLock()
	cycle := c.status.Cycles
	c.mu.RUnlock()
	forceConfig := cycle%c.cfg.ConfigRefreshEvery == 0

	var errs []error
	for _, t := range apiTunnels {
		tunnelEvents, err := c.syncTunnel(ctx, t, now, forceConfig)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		events = append(events, tunnelEvents...)
	}

	c.appendEvents(ctx, events)
	return errors.Join(errs...)
}

func (c *Collector) syncTunnel(ctx context.Context, t cloudflare.Tunnel, now time.Time, forceConfig bool) ([]store.Event, error) {
	connectors, err := c.cf.ListConnectors(ctx, t.ID)
	if err != nil {
		return nil, fmt.Errorf("connectors for %s: %w", t.Name, err)
	}

	storeConnectors := make([]store.Connector, 0, len(connectors))
	for _, conn := range connectors {
		storeConnectors = append(storeConnectors, store.Connector{
			ID:            conn.ID,
			TunnelID:      t.ID,
			Version:       conn.Version,
			Arch:          conn.Arch,
			ConfigVersion: conn.ConfigVersion,
			Features:      conn.Features,
			RunAt:         conn.RunAt.Time,
		})
	}

	connDiff, err := c.store.ReplaceConnectors(ctx, t.ID, storeConnectors, now)
	if err != nil {
		return nil, fmt.Errorf("save connectors for %s: %w", t.Name, err)
	}

	if err := c.store.ReplaceConnections(ctx, t.ID, toStoreConnections(t), now); err != nil {
		return nil, fmt.Errorf("save connections for %s: %w", t.Name, err)
	}

	events := connectorEvents(t.ID, connDiff, now)

	if forceConfig || configVersionDrifted(ctx, c, t.ID, storeConnectors) {
		changed, err := c.syncConfiguration(ctx, t.ID, now)
		if err != nil {
			return events, fmt.Errorf("configuration for %s: %w", t.Name, err)
		}
		if changed {
			events = append(events, store.Event{
				Timestamp: now,
				Kind:      store.EventConfigChanged,
				TunnelID:  t.ID,
				Message:   "Ingress configuration changed",
			})
		}
	}

	return events, nil
}

func (c *Collector) syncConfiguration(ctx context.Context, tunnelID string, now time.Time) (bool, error) {
	cfg, err := c.cf.GetConfiguration(ctx, tunnelID)
	if err != nil {
		return false, err
	}

	rules := make([]store.IngressRule, 0, len(cfg.Config.Ingress))
	for i, rule := range cfg.Config.Ingress {
		encoded := ""
		if rule.OriginRequest != nil {
			if raw, err := json.Marshal(rule.OriginRequest); err == nil {
				encoded = string(raw)
			}
		}
		rules = append(rules, store.IngressRule{
			TunnelID:      tunnelID,
			Index:         i,
			Hostname:      rule.Hostname,
			Path:          rule.Path,
			Service:       rule.Service,
			OriginRequest: encoded,
			ConfigVersion: cfg.Version,
		})
	}

	return c.store.ReplaceIngress(ctx, tunnelID, rules, now)
}

// configVersionDrifted reports whether a connector applied a configuration
// version other than the one currently stored, which means the ingress rules
// must be re-fetched even outside the periodic refresh.
func configVersionDrifted(ctx context.Context, c *Collector, tunnelID string, connectors []store.Connector) bool {
	stored, err := c.store.Ingress(ctx, tunnelID)
	if err != nil {
		c.log.Warn("reading stored ingress failed", "tunnel", tunnelID, "err", err)
		return true
	}
	if len(stored) == 0 {
		return true
	}
	want := ingressConfigVersion(stored)
	for _, conn := range connectors {
		if conn.ConfigVersion > 0 && conn.ConfigVersion != want {
			return true
		}
	}
	return false
}

func (c *Collector) appendEvents(ctx context.Context, events []store.Event) {
	if len(events) == 0 {
		return
	}
	if err := c.store.AddEvents(ctx, events); err != nil {
		c.log.Error("writing events failed", "err", err)
	}
	c.observe(ctx, events)
}

// observe hands the batch to the event sink, if one is attached. It runs after
// the write so a slow or broken sink cannot cost us the event log.
func (c *Collector) observe(ctx context.Context, events []store.Event) {
	if c.sink == nil {
		return
	}
	names, err := c.tunnelNames(ctx)
	if err != nil {
		c.log.Warn("reading tunnel names for the event sink failed", "err", err)
	}
	c.sink.Dispatch(ctx, events, names)
}

func (c *Collector) tunnelNames(ctx context.Context) (map[string]string, error) {
	tunnels, err := c.store.Tunnels(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(tunnels))
	for _, t := range tunnels {
		names[t.ID] = t.Name
	}
	return names, nil
}

func (c *Collector) prune(ctx context.Context, now time.Time) {
	cutoff := now.AddDate(0, 0, -c.cfg.RetentionDays)
	if err := c.store.Prune(ctx, cutoff); err != nil {
		c.log.Warn("pruning old records failed", "err", err)
	}
}

func toStoreTunnel(t cloudflare.Tunnel) store.Tunnel {
	return store.Tunnel{
		ID:              t.ID,
		Name:            t.Name,
		TunType:         t.TunType,
		ConfigSrc:       t.ConfigSrc,
		RemoteConfig:    t.RemoteConfig,
		Status:          t.Status,
		CreatedAt:       t.CreatedAt.Time,
		ConnsActiveAt:   t.ConnsActiveAt.Time,
		ConnsInactiveAt: t.ConnsInactiveAt.Time,
	}
}

func toStoreConnections(t cloudflare.Tunnel) []store.Connection {
	out := make([]store.Connection, 0, len(t.Connections))
	for _, conn := range t.Connections {
		id := conn.UUID
		if id == "" {
			id = conn.ID
		}
		out = append(out, store.Connection{
			UUID:        id,
			ConnectorID: conn.ClientID,
			TunnelID:    t.ID,
			ColoName:    conn.ColoName,
			OriginIP:    conn.OriginIP,
			OpenedAt:    conn.OpenedAt.Time,
		})
	}
	return out
}

func tunnelEvents(diff store.TunnelDiff, now time.Time) []store.Event {
	var events []store.Event
	added := map[string]bool{}

	for _, t := range diff.Added {
		added[t.ID] = true
		events = append(events, store.Event{
			Timestamp: now,
			Kind:      store.EventTunnelAdded,
			TunnelID:  t.ID,
			ToState:   t.Status,
			Message:   fmt.Sprintf("Tunnel %q discovered", t.Name),
		})
	}
	for _, t := range diff.Removed {
		events = append(events, store.Event{
			Timestamp: now,
			Kind:      store.EventTunnelRemoved,
			TunnelID:  t.ID,
			FromState: t.Status,
			Message:   fmt.Sprintf("Tunnel %q disappeared", t.Name),
		})
	}
	for _, change := range diff.Changes {
		// The first status of a new tunnel is already covered by its added event.
		if added[change.TunnelID] {
			continue
		}
		events = append(events, store.Event{
			Timestamp: change.ChangedAt,
			Kind:      store.EventTunnelStatus,
			TunnelID:  change.TunnelID,
			FromState: change.FromStatus,
			ToState:   change.Status,
			Message:   fmt.Sprintf("Status changed from %s to %s", change.FromStatus, change.Status),
		})
	}
	return events
}

func connectorEvents(tunnelID string, diff store.ConnectorDiff, now time.Time) []store.Event {
	var events []store.Event
	for _, c := range diff.Added {
		events = append(events, store.Event{
			Timestamp: now,
			Kind:      store.EventConnectorAdded,
			TunnelID:  tunnelID,
			ToState:   c.Version,
			Message:   fmt.Sprintf("Connector %s connected (cloudflared %s, %s)", shortID(c.ID), c.Version, c.Arch),
		})
	}
	for _, c := range diff.Removed {
		events = append(events, store.Event{
			Timestamp: now,
			Kind:      store.EventConnectorRemoved,
			TunnelID:  tunnelID,
			FromState: c.Version,
			Message:   fmt.Sprintf("Connector %s disconnected", shortID(c.ID)),
		})
	}
	for _, c := range diff.Changed {
		if c.FromVersion != c.ToVersion {
			events = append(events, store.Event{
				Timestamp: now,
				Kind:      store.EventVersionDrift,
				TunnelID:  tunnelID,
				FromState: c.FromVersion,
				ToState:   c.ToVersion,
				Message: fmt.Sprintf("Connector %s changed cloudflared %s to %s",
					shortID(c.ID), c.FromVersion, c.ToVersion),
			})
		}
		if c.FromConfigVersion != c.ToConfigVersion {
			events = append(events, store.Event{
				Timestamp: now,
				Kind:      store.EventConfigChanged,
				TunnelID:  tunnelID,
				Message: fmt.Sprintf("Connector %s applied configuration version %d",
					shortID(c.ID), c.ToConfigVersion),
			})
		}
	}
	return events
}

// jitter sleeps for a random fraction of the interval.
func jitter(ctx context.Context, interval time.Duration) error {
	spread := interval / 10
	if spread <= 0 {
		return nil
	}
	delay := time.Duration(rand.Int64N(int64(spread))) //nolint:gosec // G404: scheduling jitter needs no cryptographic randomness
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
