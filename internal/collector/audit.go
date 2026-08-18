package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/daknoblo/CFTM/internal/cloudflare"
	"github.com/daknoblo/CFTM/internal/store"
)

// Meta keys used to cache background results in the store.
const (
	MetaLatestRelease = "cloudflared_latest"
	MetaAccessAuditAt = "access_audit_at"
	MetaTunnelAlerts  = "tunnel_alert_coverage"
)

// AlertCoverage records which tunnels a Cloudflare notification policy would
// fire for. Readable is false when the token cannot list policies, in which
// case an empty Covered must not be read as "nothing is alerted".
type AlertCoverage struct {
	Readable bool     `json:"readable"`
	Covered  []string `json:"covered"`
}

// CoversTunnel reports whether an alert would fire for a tunnel.
func (a AlertCoverage) CoversTunnel(id string) bool {
	for _, covered := range a.Covered {
		if covered == id {
			return true
		}
	}
	return false
}

// ReleaseChecker resolves the newest published cloudflared version.
type ReleaseChecker interface {
	Latest(ctx context.Context) (string, error)
}

// CheckRelease refreshes the cached cloudflared release once.
func (c *Collector) CheckRelease(ctx context.Context, checker ReleaseChecker) error {
	latest, err := checker.Latest(ctx)
	if err != nil {
		return fmt.Errorf("cloudflared release lookup: %w", err)
	}
	previous, _, err := c.store.GetMeta(ctx, MetaLatestRelease)
	if err != nil {
		c.log.Warn("reading cached release failed", "err", err)
	}
	if err := c.store.SetMeta(ctx, MetaLatestRelease, latest, time.Now()); err != nil {
		return fmt.Errorf("caching release: %w", err)
	}
	if previous != "" && previous != latest {
		c.log.Info("new cloudflared release", "from", previous, "to", latest)
	}
	return nil
}

// RunReleaseChecks keeps the cached cloudflared release up to date until the
// context is canceled.
func (c *Collector) RunReleaseChecks(ctx context.Context, checker ReleaseChecker, interval time.Duration) {
	run := func() {
		if err := c.CheckRelease(ctx, checker); err != nil {
			c.log.Warn("cloudflared release check failed", "err", err)
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

// LatestRelease returns the cached cloudflared version, or an empty string when
// no lookup has succeeded yet.
func (c *Collector) LatestRelease(ctx context.Context) string {
	latest, _, err := c.store.GetMeta(ctx, MetaLatestRelease)
	if err != nil {
		c.log.Warn("reading cached release failed", "err", err)
		return ""
	}
	return latest
}

// RunAccessAudit refreshes the Access application and service token inventory
// until the context is canceled.
func (c *Collector) RunAccessAudit(ctx context.Context, interval time.Duration) {
	run := func() {
		if err := c.AuditAccess(ctx); err != nil {
			c.log.Warn("access audit failed", "err", err)
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

// AuditAccess fetches every Access application with its policies plus the
// service token inventory, and stores a flattened summary.
func (c *Collector) AuditAccess(ctx context.Context) error {
	now := time.Now()

	apps, err := c.cf.ListAccessApps(ctx)
	c.record(ctx, CapAccessApps, err)
	if err != nil {
		return fmt.Errorf("list access apps: %w", err)
	}

	var errs []error
	stored := make([]store.AccessApp, 0, len(apps))
	for _, app := range apps {
		summary := store.AccessApp{
			ID:         app.ID,
			Name:       app.Name,
			Domains:    app.Domains(),
			RawDomains: app.RawDomains(),
			Type:       app.Type,
		}

		policies, err := c.cf.ListAccessPolicies(ctx, app.ID)
		if err != nil {
			// A single unreadable application must not void the whole audit.
			errs = append(errs, fmt.Errorf("policies for %s: %w", app.Name, err))
		} else {
			summary.PolicyCount = len(policies)
			for _, p := range policies {
				if p.IsBypass() {
					summary.HasBypass = true
					summary.BypassPolicies = append(summary.BypassPolicies, p.Name)
				}
				if p.HasServiceTokenRule() {
					summary.HasToken = true
				}
			}
		}
		stored = append(stored, summary)
	}

	if err := c.store.ReplaceAccessApps(ctx, stored, now); err != nil {
		return fmt.Errorf("save access apps: %w", err)
	}

	tokens, err := c.cf.ListServiceTokens(ctx)
	c.record(ctx, CapServiceTokens, err)
	if err != nil {
		errs = append(errs, fmt.Errorf("list service tokens: %w", err))
	} else if err := c.store.ReplaceServiceTokens(ctx, toStoreTokens(tokens), now); err != nil {
		return fmt.Errorf("save service tokens: %w", err)
	}

	if err := c.store.SetMeta(ctx, MetaAccessAuditAt, now.UTC().Format(time.RFC3339), now); err != nil {
		return fmt.Errorf("record audit timestamp: %w", err)
	}

	if err := c.auditAlerts(ctx, now); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// auditAlerts records which tunnels Cloudflare itself would alert on. The token
// may not carry the Notifications permission, which is not an error: the
// coverage is simply unknown and no finding is raised.
func (c *Collector) auditAlerts(ctx context.Context, now time.Time) error {
	coverage := AlertCoverage{Readable: true}

	policies, err := c.cf.ListNotificationPolicies(ctx)
	c.record(ctx, CapNotifications, err)
	if err != nil {
		c.log.Debug("listing notification policies failed", "err", err)
		coverage.Readable = false
	} else {
		tunnels, err := c.store.Tunnels(ctx)
		if err != nil {
			return fmt.Errorf("read tunnels for alert coverage: %w", err)
		}
		for _, t := range tunnels {
			if alertedOn(policies, t.ID, t.Name) {
				coverage.Covered = append(coverage.Covered, t.ID)
			}
		}
	}

	raw, err := json.Marshal(coverage)
	if err != nil {
		return fmt.Errorf("encode alert coverage: %w", err)
	}
	if err := c.store.SetMeta(ctx, MetaTunnelAlerts, string(raw), now); err != nil {
		return fmt.Errorf("cache alert coverage: %w", err)
	}
	return nil
}

// alertedOn reports whether a health alert would reach someone for this tunnel.
// A policy with no destination is configured but silent, so it does not count.
func alertedOn(policies []cloudflare.NotificationPolicy, tunnelID, tunnelName string) bool {
	for _, p := range policies {
		if p.AlertType != cloudflare.AlertTunnelHealth {
			continue
		}
		if p.Covers(tunnelID, tunnelName) && p.DeliversTo() > 0 {
			return true
		}
	}
	return false
}

// TunnelAlerts returns the cached alert coverage.
func (c *Collector) TunnelAlerts(ctx context.Context) AlertCoverage {
	raw, _, err := c.store.GetMeta(ctx, MetaTunnelAlerts)
	if err != nil || raw == "" {
		return AlertCoverage{}
	}
	var coverage AlertCoverage
	if err := json.Unmarshal([]byte(raw), &coverage); err != nil {
		c.log.Warn("decoding cached alert coverage failed", "err", err)
		return AlertCoverage{}
	}
	return coverage
}

// AccessAuditAt returns when the Access inventory was last refreshed.
func (c *Collector) AccessAuditAt(ctx context.Context) time.Time {
	raw, _, err := c.store.GetMeta(ctx, MetaAccessAuditAt)
	if err != nil || raw == "" {
		return time.Time{}
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return at
}

func toStoreTokens(tokens []cloudflare.ServiceToken) []store.ServiceToken {
	out := make([]store.ServiceToken, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, store.ServiceToken{
			ID:        t.ID,
			Name:      t.Name,
			ClientID:  t.ClientID,
			ExpiresAt: t.ExpiresAt.Time,
		})
	}
	return out
}
