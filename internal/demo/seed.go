package demo

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http/httptest"
	"time"

	"github.com/daknoblo/CFTM/internal/cloudflare"
	"github.com/daknoblo/CFTM/internal/collector"
	"github.com/daknoblo/CFTM/internal/prober"
	"github.com/daknoblo/CFTM/internal/store"
)

// Seed fills a store with the fixture account by driving the real collector, so
// the demo cannot show anything the production code path would not produce.
// The returned collector is the one the server has to be wired to, because the
// poll status lives in its memory.
func Seed(ctx context.Context, st *store.Store, log *slog.Logger) (*collector.Collector, func(), error) {
	now := time.Now()

	api := httptest.NewServer(Handler(now))
	cf := cloudflare.New(AccountID, "demo-token-never-used",
		cloudflare.WithBaseURL(api.URL),
		cloudflare.WithHTTPClient(api.Client()),
		cloudflare.WithLogger(log),
	)
	coll := collector.New(cf, st, collector.Config{
		PollInterval:       30 * time.Second,
		ConfigRefreshEvery: 10,
		RetentionDays:      90,
	}, log)

	if err := backfillHistory(ctx, st, now); err != nil {
		api.Close()
		return nil, nil, fmt.Errorf("backfill history: %w", err)
	}

	if err := st.SetMeta(ctx, collector.MetaLatestRelease, latestCloudflared, now); err != nil {
		api.Close()
		return nil, nil, fmt.Errorf("seed release: %w", err)
	}

	coll.PollOnce(ctx)
	if err := coll.AuditAccess(ctx); err != nil {
		api.Close()
		return nil, nil, fmt.Errorf("audit access: %w", err)
	}
	if err := coll.ProbeOnce(ctx, fakeProber{}); err != nil {
		api.Close()
		return nil, nil, fmt.Errorf("probe: %w", err)
	}

	// One muted finding so the feature is visible without clicking.
	err := st.IgnoreFinding(ctx, store.FindingRef{
		Code:     "access_unprotected",
		TunnelID: tunnelEdge,
		Hostname: "feed.example.com",
	}, "Hostname is publicly reachable without a Cloudflare Access application", now)
	if err != nil {
		api.Close()
		return nil, nil, fmt.Errorf("mute finding: %w", err)
	}

	return coll, api.Close, nil
}

// backfillHistory writes a day of status transitions so the heartbeat bar and
// the uptime figures have something to show. SaveTunnels takes the observation
// time, so replaying it with past timestamps produces genuine history rows.
func backfillHistory(ctx context.Context, st *store.Store, now time.Time) error {
	// A deterministic source keeps the exported site stable between runs.
	rng := rand.New(rand.NewPCG(42, 1024)) //nolint:gosec // G404: demo data needs no cryptographic randomness

	start := now.Add(-30 * 24 * time.Hour)
	for at := start; at.Before(now); at = at.Add(30 * time.Minute) {
		snapshot := []store.Tunnel{
			{ID: tunnelEdge, Name: "edge-frankfurt", Status: edgeStatusAt(at, now, rng)},
			{ID: tunnelHome, Name: "home-lab", Status: homeStatusAt(at, now)},
			{ID: tunnelLab, Name: "staging", Status: labStatusAt(at, now)},
		}
		if _, err := st.SaveTunnels(ctx, snapshot, at); err != nil {
			return err
		}
	}
	return nil
}

// edgeStatusAt keeps the flagship tunnel healthy apart from two short blips, so
// the uptime lands just under 100% instead of at a suspicious exactly 100.
func edgeStatusAt(at, now time.Time, rng *rand.Rand) string {
	since := now.Sub(at)
	switch {
	case since > 6*24*time.Hour && since < 6*24*time.Hour+time.Hour:
		return cloudflare.StatusDegraded
	case since > 15*time.Hour && since < 16*time.Hour:
		return cloudflare.StatusDown
	case rng.IntN(200) == 0:
		return cloudflare.StatusDegraded
	default:
		return cloudflare.StatusHealthy
	}
}

// homeStatusAt has the home tunnel drop to two connections a few hours ago and
// stay there, which is what the dashboard shows as degraded.
func homeStatusAt(at, now time.Time) string {
	if now.Sub(at) < 4*time.Hour {
		return cloudflare.StatusDegraded
	}
	return cloudflare.StatusHealthy
}

// labStatusAt takes the staging tunnel down recently, after a stretch of being
// inactive over the weekend.
func labStatusAt(at, now time.Time) string {
	since := now.Sub(at)
	switch {
	case since < 95*time.Minute:
		return cloudflare.StatusDown
	case since < 3*24*time.Hour:
		return cloudflare.StatusInactive
	default:
		return cloudflare.StatusHealthy
	}
}

// fakeProber returns canned outcomes instead of reaching the network, so the
// demo can be built offline and always looks the same.
type fakeProber struct{}

func (fakeProber) HasServiceToken() bool { return true }

func (fakeProber) ProbeAll(_ context.Context, targets []prober.Target) []store.ProbeResult {
	// A fixed table beats randomness here: every class the UI can render should
	// appear at least once.
	outcomes := map[string]struct {
		class   string
		status  int
		latency int
		err     string
	}{
		"media.example.com":   {prober.ClassOriginError, 502, 61, "origin did not answer"},
		"preview.example.com": {prober.ClassTunnelDown, 530, 38, "Cloudflare error 1033: no connector available"},
		"feed.example.com":    {prober.ClassOK, 200, 74, ""},
		"home.example.com":    {prober.ClassTimeout, 0, 10000, "context deadline exceeded"},
	}

	now := time.Now()
	results := make([]store.ProbeResult, 0, len(targets))
	for i, t := range targets {
		r := store.ProbeResult{Hostname: t.Hostname, CheckedAt: now}
		if o, ok := outcomes[t.Hostname]; ok {
			r.Class, r.StatusCode, r.LatencyMS, r.Error = o.class, o.status, o.latency, o.err
		} else {
			r.Class, r.StatusCode = prober.ClassOK, 200
			r.LatencyMS = 45 + i*17%120
		}
		results = append(results, r)
	}
	return results
}
