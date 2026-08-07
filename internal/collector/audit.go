package collector

import (
	"context"
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
)

// ReleaseChecker resolves the newest published cloudflared version.
type ReleaseChecker interface {
	Latest(ctx context.Context) (string, error)
}

// RunReleaseChecks keeps the cached cloudflared release up to date until the
// context is canceled.
func (c *Collector) RunReleaseChecks(ctx context.Context, checker ReleaseChecker, interval time.Duration) {
	run := func() {
		latest, err := checker.Latest(ctx)
		if err != nil {
			c.log.Warn("cloudflared release lookup failed", "err", err)
			return
		}
		previous, _, err := c.store.GetMeta(ctx, MetaLatestRelease)
		if err != nil {
			c.log.Warn("reading cached release failed", "err", err)
		}
		if err := c.store.SetMeta(ctx, MetaLatestRelease, latest, time.Now()); err != nil {
			c.log.Error("caching release failed", "err", err)
			return
		}
		if previous != "" && previous != latest {
			c.log.Info("new cloudflared release", "from", previous, "to", latest)
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
	if err != nil {
		return fmt.Errorf("list access apps: %w", err)
	}

	var errs []error
	stored := make([]store.AccessApp, 0, len(apps))
	for _, app := range apps {
		summary := store.AccessApp{
			ID:      app.ID,
			Name:    app.Name,
			Domains: app.Domains(),
			Type:    app.Type,
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
	if err != nil {
		errs = append(errs, fmt.Errorf("list service tokens: %w", err))
	} else if err := c.store.ReplaceServiceTokens(ctx, toStoreTokens(tokens), now); err != nil {
		return fmt.Errorf("save service tokens: %w", err)
	}

	if err := c.store.SetMeta(ctx, MetaAccessAuditAt, now.UTC().Format(time.RFC3339), now); err != nil {
		return fmt.Errorf("record audit timestamp: %w", err)
	}

	return errors.Join(errs...)
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
