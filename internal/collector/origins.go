package collector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/daknoblo/CFTM/internal/cloudflare"
	"github.com/daknoblo/CFTM/internal/store"
)

// MetaRequestOriginsAt records when the origin summary was last built.
const MetaRequestOriginsAt = "request_origins_at"

// MetaOriginWindow records the window the summary actually covers, which the
// plan may have shortened.
const MetaOriginWindow = "request_origins_window"

// RunRequestOrigins refreshes the origin summary until the context is canceled.
func (c *Collector) RunRequestOrigins(ctx context.Context, interval, window time.Duration) {
	run := func() {
		if err := c.CollectRequestOrigins(ctx, window, time.Now()); err != nil {
			c.log.Warn("request origin collection failed", "err", err)
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

// CollectRequestOrigins summarizes where requests came from. The two sources
// see different traffic and either may be unavailable, so one failing must not
// void the other.
func (c *Collector) CollectRequestOrigins(ctx context.Context, window time.Duration, now time.Time) error {
	var errs []error

	origins, effective, err := c.httpOrigins(ctx, window, now)
	if err != nil {
		errs = append(errs, err)
	}

	logins, err := c.loginOrigins(ctx, window, now)
	if err != nil {
		errs = append(errs, err)
	}
	origins = append(origins, logins...)

	// Without a single reachable source the old summary is better than none.
	if len(origins) == 0 && len(errs) > 0 {
		return errors.Join(errs...)
	}

	previous, err := c.knownCountries(ctx)
	if err != nil {
		errs = append(errs, err)
	}

	if err := c.store.ReplaceRequestOrigins(ctx, origins); err != nil {
		return errors.Join(append(errs, fmt.Errorf("save request origins: %w", err))...)
	}
	c.appendEvents(ctx, c.newCountryEvents(origins, previous, now))

	if effective == 0 {
		effective = window
	}
	if err := c.store.SetMeta(ctx, MetaOriginWindow, effective.String(), now); err != nil {
		errs = append(errs, err)
	}
	if err := c.store.SetMeta(ctx, MetaRequestOriginsAt, now.UTC().Format(time.RFC3339), now); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// httpOrigins reads the zone-scoped analytics, the only source that also sees
// traffic an Access bypass policy waves through. It returns the window that
// survived the plan limits.
func (c *Collector) httpOrigins(ctx context.Context, window time.Duration, now time.Time) ([]store.RequestOrigin, time.Duration, error) {
	rules, err := c.store.Ingress(ctx, "")
	if err != nil {
		return nil, 0, fmt.Errorf("read ingress: %w", err)
	}
	served := servedHostnames(rules)
	if len(served) == 0 {
		return nil, 0, nil
	}

	zones, err := c.cf.ListZones(ctx)
	c.record(ctx, CapZones, err)
	if err != nil {
		return nil, 0, fmt.Errorf("list zones: %w", err)
	}

	var (
		out       []store.RequestOrigin
		effective time.Duration
		errs      []error
		capErr    error
	)
	for _, zone := range zonesServing(zones, served) {
		limits, err := c.cf.HTTPRequestsLimits(ctx, zone.ID)
		if err != nil {
			capErr = err
			errs = append(errs, fmt.Errorf("analytics limits for %s: %w", zone.Name, err))
			continue
		}

		zoneWindow := limits.ClampWindow(window)
		if effective == 0 || zoneWindow < effective {
			effective = zoneWindow
		}

		groups, err := c.cf.HTTPRequestsByCountry(ctx, zone.ID, now.Add(-zoneWindow), now)
		if err != nil {
			capErr = err
			errs = append(errs, fmt.Errorf("http analytics for %s: %w", zone.Name, err))
			continue
		}
		out = append(out, toHTTPOrigins(groups, served, now.Add(-zoneWindow), now)...)
	}

	c.record(ctx, CapZoneAnalytics, capErr)
	return out, effective, errors.Join(errs...)
}

// loginOrigins reads the account-scoped Access login analytics, which unlike
// the REST audit log also covers non-identity attempts.
func (c *Collector) loginOrigins(ctx context.Context, window time.Duration, now time.Time) ([]store.RequestOrigin, error) {
	groups, err := c.cf.AccessLoginsByCountry(ctx, now.Add(-window), now)
	c.record(ctx, CapLoginAnalytics, err)
	if err != nil {
		return nil, fmt.Errorf("access login analytics: %w", err)
	}

	byCountry := map[string]*store.RequestOrigin{}
	for _, g := range groups {
		country := normalizeCountry(g.Country)
		entry, ok := byCountry[country]
		if !ok {
			entry = &store.RequestOrigin{
				Source:  store.OriginSourceLogin,
				Country: country,
				Window:  now.Add(-window),
				LastAt:  now,
			}
			byCountry[country] = entry
		}
		entry.Requests += g.Attempts
		if g.Succeeded {
			entry.Allowed += g.Attempts
		} else {
			entry.Denied += g.Attempts
		}
	}

	out := make([]store.RequestOrigin, 0, len(byCountry))
	for _, entry := range byCountry {
		out = append(out, *entry)
	}
	return out, nil
}

// toHTTPOrigins keeps only the hostnames this account actually serves, since
// the zone answers for everything published under it.
func toHTTPOrigins(groups []cloudflare.OriginGroup, served map[string]bool, from, to time.Time) []store.RequestOrigin {
	out := make([]store.RequestOrigin, 0, len(groups))
	for _, g := range groups {
		host := strings.ToLower(g.Hostname)
		if !served[host] {
			continue
		}
		origin := store.RequestOrigin{
			Source:   store.OriginSourceHTTP,
			Hostname: host,
			Path:     g.Path,
			Country:  normalizeCountry(g.Country),
			Requests: g.Requests,
			Sampled:  g.Sampled,
			Window:   from,
			LastAt:   to,
		}
		// A 3xx cannot be told apart from an ordinary redirect, so only an
		// outright error status counts as a refusal.
		if g.Status >= 400 {
			origin.Denied = g.Requests
		} else {
			origin.Allowed = g.Requests
		}
		out = append(out, origin)
	}
	return out
}

// newCountryEvents records a country showing up for the first time, unless the
// operator declared it as expected.
func (c *Collector) newCountryEvents(origins []store.RequestOrigin, previous map[string]bool, now time.Time) []store.Event {
	seen := map[string]bool{}
	var events []store.Event
	for _, o := range origins {
		if o.Country == "" || o.Country == store.UnknownCountry {
			continue
		}
		if seen[o.Country] || previous[o.Country] || c.cfg.ExpectedCountries[strings.ToLower(o.Country)] {
			continue
		}
		seen[o.Country] = true
		events = append(events, store.Event{
			Timestamp: now,
			Kind:      store.EventOriginCountry,
			Hostname:  o.Hostname,
			ToState:   o.Country,
			Message:   fmt.Sprintf("First requests seen from %s", o.Country),
		})
	}
	return events
}

func (c *Collector) knownCountries(ctx context.Context) (map[string]bool, error) {
	stored, err := c.store.RequestOrigins(ctx)
	if err != nil {
		return nil, fmt.Errorf("read stored origins: %w", err)
	}
	known := make(map[string]bool, len(stored))
	for _, o := range stored {
		known[o.Country] = true
	}
	return known, nil
}

// servedHostnames lists the HTTP hostnames of every tunnel, lowercased.
func servedHostnames(rules []store.IngressRule) map[string]bool {
	out := map[string]bool{}
	for _, r := range rules {
		if r.Hostname == "" || !isHTTPService(r.Service) {
			continue
		}
		out[strings.ToLower(r.Hostname)] = true
	}
	return out
}

// zonesServing narrows the account's zones to those that actually carry one of
// our hostnames, so an unrelated zone costs no analytics query.
func zonesServing(zones []cloudflare.Zone, hostnames map[string]bool) []cloudflare.Zone {
	wanted := map[string]cloudflare.Zone{}
	for host := range hostnames {
		if zone, ok := cloudflare.ZoneFor(zones, host); ok {
			wanted[zone.ID] = zone
		}
	}

	out := make([]cloudflare.Zone, 0, len(wanted))
	for _, zone := range zones {
		if _, ok := wanted[zone.ID]; ok {
			out = append(out, zone)
		}
	}
	return out
}

// normalizeCountry uppercases the ISO code and names the unknown case, which
// the edge reports for traffic it could not place.
func normalizeCountry(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" || code == "XX" || code == "T1" {
		return store.UnknownCountry
	}
	return code
}

// RequestOriginsAt returns when the origin summary was last built.
func (c *Collector) RequestOriginsAt(ctx context.Context) time.Time {
	raw, _, err := c.store.GetMeta(ctx, MetaRequestOriginsAt)
	if err != nil || raw == "" {
		return time.Time{}
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return at
}

// OriginWindow returns the window the summary covers, which the plan limits
// may have shortened below the configured one.
func (c *Collector) OriginWindow(ctx context.Context) time.Duration {
	raw, _, err := c.store.GetMeta(ctx, MetaOriginWindow)
	if err != nil || raw == "" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0
	}
	return d
}
