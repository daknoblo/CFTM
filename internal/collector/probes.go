package collector

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/daknoblo/CFTM/internal/prober"
	"github.com/daknoblo/CFTM/internal/store"
)

// Prober checks published hostnames end to end.
type Prober interface {
	ProbeAll(ctx context.Context, targets []prober.Target) []store.ProbeResult
	HasServiceToken() bool
}

// RunProbes checks every probeable hostname until the context is canceled.
func (c *Collector) RunProbes(ctx context.Context, p Prober, interval time.Duration) {
	if !p.HasServiceToken() {
		c.log.Warn("probing without an Access service token; results will only show that the Cloudflare edge is reachable")
	}

	run := func() {
		if err := c.ProbeOnce(ctx, p); err != nil {
			c.log.Warn("probe round failed", "err", err)
		}
	}

	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

// ProbeOnce runs a single probe round and records class transitions as events.
func (c *Collector) ProbeOnce(ctx context.Context, p Prober) error {
	rules, err := c.store.Ingress(ctx, "")
	if err != nil {
		return fmt.Errorf("read ingress: %w", err)
	}

	publicHostnames, err := c.publicHostnames(ctx, rules)
	if err != nil {
		return err
	}

	targets := prober.Targets(rules, publicHostnames)
	if len(targets) == 0 {
		return nil
	}

	previous, err := c.store.LatestProbeResults(ctx)
	if err != nil {
		return fmt.Errorf("read previous probes: %w", err)
	}

	results := p.ProbeAll(ctx, targets)
	if err := c.store.AddProbeResults(ctx, results); err != nil {
		return fmt.Errorf("save probe results: %w", err)
	}

	hostToTunnel := map[string]string{}
	for _, rule := range rules {
		if _, ok := hostToTunnel[rule.Hostname]; !ok {
			hostToTunnel[rule.Hostname] = rule.TunnelID
		}
	}

	var events []store.Event
	for _, r := range results {
		prev, existed := previous[r.Hostname]
		if existed && prev.Class == r.Class {
			continue
		}
		events = append(events, store.Event{
			Timestamp: r.CheckedAt,
			Kind:      store.EventProbe,
			TunnelID:  hostToTunnel[r.Hostname],
			Hostname:  r.Hostname,
			FromState: prev.Class,
			ToState:   r.Class,
			Message:   probeMessage(r),
		})
	}
	c.appendEvents(ctx, events)

	return nil
}

func probeMessage(r store.ProbeResult) string {
	if r.Error != "" {
		return fmt.Sprintf("Probe returned %s: %s", r.Class, r.Error)
	}
	return fmt.Sprintf("Probe returned %s (HTTP %d, %dms)", r.Class, r.StatusCode, r.LatencyMS)
}

// publicHostnames names the ingress hostnames that have no Access application.
// It stays empty until an audit has run, because an empty inventory is
// indistinguishable from an account without any Access application.
func (c *Collector) publicHostnames(ctx context.Context, rules []store.IngressRule) (map[string]bool, error) {
	if c.AccessAuditAt(ctx).IsZero() {
		return nil, nil
	}

	apps, err := c.store.AccessApps(ctx)
	if err != nil {
		return nil, fmt.Errorf("read access apps: %w", err)
	}

	protected := make(map[string]bool, len(apps))
	for _, app := range apps {
		for _, domain := range app.Domains {
			protected[strings.ToLower(domain)] = true
		}
	}

	out := map[string]bool{}
	for _, rule := range rules {
		host := strings.ToLower(rule.Hostname)
		if host != "" && !protected[host] {
			out[host] = true
		}
	}
	return out, nil
}
