package collector

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/daknoblo/CFTM/internal/cloudflare"
	"github.com/daknoblo/CFTM/internal/store"
)

// MetaAccessLoginsAt records when the authentication summary was last built.
const MetaAccessLoginsAt = "access_logins_at"

// RunAccessLogins refreshes the authentication summary until the context is
// canceled.
func (c *Collector) RunAccessLogins(ctx context.Context, interval, window time.Duration) {
	run := func() {
		if err := c.CollectAccessLogins(ctx, window, time.Now()); err != nil {
			c.log.Warn("access login collection failed", "err", err)
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

// CollectAccessLogins summarizes the Access authentication log. It is opt-in
// because it needs a token permission the rest of CFTM does not.
func (c *Collector) CollectAccessLogins(ctx context.Context, window time.Duration, now time.Time) error {
	requests, err := c.cf.ListAccessRequests(ctx, now.Add(-window))
	c.record(ctx, CapAccessLogins, err)
	if err != nil {
		return fmt.Errorf("list access requests: %w", err)
	}

	if err := c.store.ReplaceAccessLogins(ctx, summarizeLogins(requests)); err != nil {
		return fmt.Errorf("save access logins: %w", err)
	}
	return c.store.SetMeta(ctx, MetaAccessLoginsAt, now.UTC().Format(time.RFC3339), now)
}

// summarizeLogins folds the raw events into one row per application. Distinct
// users are counted rather than events, so one person reloading a page all day
// does not read as heavy usage.
func summarizeLogins(requests []cloudflare.AccessRequest) []store.AccessLogin {
	type acc struct {
		login store.AccessLogin
		users map[string]bool
	}

	byApp := map[string]*acc{}
	for _, r := range requests {
		domain := normalizeAppDomain(r.AppDomain)
		if domain == "" {
			continue
		}
		entry, ok := byApp[domain]
		if !ok {
			entry = &acc{
				login: store.AccessLogin{AppDomain: domain},
				users: map[string]bool{},
			}
			byApp[domain] = entry
		}

		if r.Allowed {
			entry.login.Allowed++
		} else {
			entry.login.Denied++
		}
		if r.UserEmail != "" {
			entry.users[strings.ToLower(r.UserEmail)] = true
		}
		if r.CreatedAt.After(entry.login.LastAt) {
			entry.login.LastAt = r.CreatedAt.Time
		}
	}

	out := make([]store.AccessLogin, 0, len(byApp))
	for _, entry := range byApp {
		entry.login.Users = len(entry.users)
		out = append(out, entry.login)
	}
	return out
}

// normalizeAppDomain strips the path Cloudflare appends to an application
// domain, so "app.example.com/admin" and "app.example.com" are one row.
func normalizeAppDomain(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	host, _, _ := strings.Cut(domain, "/")
	return host
}

// AccessLoginsAt returns when the authentication summary was last built.
func (c *Collector) AccessLoginsAt(ctx context.Context) time.Time {
	raw, _, err := c.store.GetMeta(ctx, MetaAccessLoginsAt)
	if err != nil || raw == "" {
		return time.Time{}
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return at
}
