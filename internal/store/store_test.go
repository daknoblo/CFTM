package store

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "cftm.db"))
	if err != nil {
		t.Fatalf("Open() error = %v, want nil", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cftm.db")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	_ = first.Close()

	second, err := Open(path)
	if err != nil {
		t.Fatalf("second Open() error = %v, want nil", err)
	}
	defer func() { _ = second.Close() }()

	if _, err := second.Tunnels(t.Context()); err != nil {
		t.Errorf("Tunnels() after reopen error = %v, want nil", err)
	}
}

func TestSaveTunnelsTracksTransitions(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	tunnel := Tunnel{ID: "t1", Name: "edge.example.com", Status: "healthy", TunType: "cfd_tunnel"}

	diff, err := s.SaveTunnels(ctx, []Tunnel{tunnel}, base)
	if err != nil {
		t.Fatalf("SaveTunnels() error = %v", err)
	}
	if len(diff.Added) != 1 || len(diff.Changes) != 1 {
		t.Fatalf("first snapshot diff = %+v, want 1 added and 1 change", diff)
	}
	if diff.Changes[0].FromStatus != "" || diff.Changes[0].Status != "healthy" {
		t.Errorf("initial change = %+v, want empty -> healthy", diff.Changes[0])
	}

	// An unchanged snapshot must not append history.
	diff, err = s.SaveTunnels(ctx, []Tunnel{tunnel}, base.Add(time.Minute))
	if err != nil {
		t.Fatalf("SaveTunnels() error = %v", err)
	}
	if len(diff.Changes) != 0 || len(diff.Added) != 0 {
		t.Errorf("unchanged snapshot diff = %+v, want empty", diff)
	}

	tunnel.Status = "down"
	diff, err = s.SaveTunnels(ctx, []Tunnel{tunnel}, base.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("SaveTunnels() error = %v", err)
	}
	if len(diff.Changes) != 1 || diff.Changes[0].FromStatus != "healthy" || diff.Changes[0].Status != "down" {
		t.Errorf("changes = %+v, want healthy -> down", diff.Changes)
	}

	history, err := s.StatusHistory(ctx, "t1", base.Add(-time.Hour))
	if err != nil {
		t.Fatalf("StatusHistory() error = %v", err)
	}
	if len(history) != 2 {
		t.Errorf("len(history) = %d, want 2", len(history))
	}

	stored, err := s.Tunnels(ctx)
	if err != nil {
		t.Fatalf("Tunnels() error = %v", err)
	}
	if len(stored) != 1 || stored[0].Status != "down" {
		t.Errorf("stored = %+v, want one tunnel in state down", stored)
	}
	if !stored[0].FirstSeen.Equal(base) {
		t.Errorf("FirstSeen = %v, want %v (must not move on update)", stored[0].FirstSeen, base)
	}
}

func TestSaveTunnelsRemovesVanishedTunnels(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	if _, err := s.SaveTunnels(ctx, []Tunnel{{ID: "a", Status: "healthy"}, {ID: "b", Status: "healthy"}}, now); err != nil {
		t.Fatalf("SaveTunnels() error = %v", err)
	}
	if _, err := s.ReplaceIngress(ctx, "b", []IngressRule{{TunnelID: "b", Index: 0, Hostname: "x.example.com", Service: "http://127.0.0.1:1"}}, now); err != nil {
		t.Fatalf("ReplaceIngress() error = %v", err)
	}

	diff, err := s.SaveTunnels(ctx, []Tunnel{{ID: "a", Status: "healthy"}}, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("SaveTunnels() error = %v", err)
	}
	if len(diff.Removed) != 1 || diff.Removed[0].ID != "b" {
		t.Fatalf("removed = %+v, want tunnel b", diff.Removed)
	}

	ingress, err := s.Ingress(ctx, "")
	if err != nil {
		t.Fatalf("Ingress() error = %v", err)
	}
	if len(ingress) != 0 {
		t.Errorf("ingress = %+v, want the removed tunnel's rules to be gone", ingress)
	}
}

func TestReplaceConnectorsDiff(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	first := []Connector{
		{ID: "c1", Version: "2026.4.0", Arch: "linux_amd64", ConfigVersion: 7, Features: []string{"ha-origin"}},
		{ID: "c2", Version: "2026.4.0"},
	}
	diff, err := s.ReplaceConnectors(ctx, "t1", first, now)
	if err != nil {
		t.Fatalf("ReplaceConnectors() error = %v", err)
	}
	if len(diff.Added) != 2 || len(diff.Removed) != 0 {
		t.Fatalf("diff = %+v, want 2 added", diff)
	}

	diff, err = s.ReplaceConnectors(ctx, "t1", []Connector{first[0], {ID: "c3"}}, now)
	if err != nil {
		t.Fatalf("ReplaceConnectors() error = %v", err)
	}
	if len(diff.Added) != 1 || diff.Added[0].ID != "c3" {
		t.Errorf("added = %+v, want c3", diff.Added)
	}
	if len(diff.Removed) != 1 || diff.Removed[0].ID != "c2" {
		t.Errorf("removed = %+v, want c2", diff.Removed)
	}

	stored, err := s.Connectors(ctx, "t1")
	if err != nil {
		t.Fatalf("Connectors() error = %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("len(connectors) = %d, want 2", len(stored))
	}
	if len(stored[0].Features) != 1 || stored[0].Features[0] != "ha-origin" {
		t.Errorf("features = %v, want [ha-origin]", stored[0].Features)
	}
}

func TestReplaceIngressDetectsChange(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	rules := []IngressRule{
		{TunnelID: "t1", Index: 0, Hostname: "a.example.com", Service: "http://127.0.0.1:8080", ConfigVersion: 3},
		{TunnelID: "t1", Index: 1, Service: "http_status:404", ConfigVersion: 3},
	}

	changed, err := s.ReplaceIngress(ctx, "t1", rules, now)
	if err != nil {
		t.Fatalf("ReplaceIngress() error = %v", err)
	}
	if !changed {
		t.Error("changed = false on first write, want true")
	}

	changed, err = s.ReplaceIngress(ctx, "t1", rules, now)
	if err != nil {
		t.Fatalf("ReplaceIngress() error = %v", err)
	}
	if changed {
		t.Error("changed = true for an identical rule set, want false")
	}

	rules[0].Service = "http://127.0.0.1:9090"
	changed, err = s.ReplaceIngress(ctx, "t1", rules, now)
	if err != nil {
		t.Fatalf("ReplaceIngress() error = %v", err)
	}
	if !changed {
		t.Error("changed = false after a service change, want true")
	}
}

func TestUptimeFor(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	// Healthy for 22h, down for 1h, healthy again for the final hour.
	steps := []struct {
		at     time.Time
		status string
	}{
		{now.Add(-24 * time.Hour), "healthy"},
		{now.Add(-2 * time.Hour), "down"},
		{now.Add(-1 * time.Hour), "healthy"},
	}
	for _, step := range steps {
		if _, err := s.SaveTunnels(ctx, []Tunnel{{ID: "t1", Status: step.status}}, step.at); err != nil {
			t.Fatalf("SaveTunnels() error = %v", err)
		}
	}

	up, err := s.UptimeFor(ctx, "t1", 24*time.Hour, now)
	if err != nil {
		t.Fatalf("UptimeFor() error = %v", err)
	}
	if !up.Observed {
		t.Fatal("Observed = false, want true")
	}
	if want := 23.0 / 24.0 * 100; math.Abs(up.Percent()-want) > 0.01 {
		t.Errorf("Percent() = %.4f, want %.4f", up.Percent(), want)
	}
	if up.Changes != 2 {
		t.Errorf("Changes = %d, want 2", up.Changes)
	}
}

func TestUptimeForClampsToFirstObservation(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	// Only two hours of history, but a 30-day window is requested.
	if _, err := s.SaveTunnels(ctx, []Tunnel{{ID: "t1", Status: "healthy"}}, now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("SaveTunnels() error = %v", err)
	}

	up, err := s.UptimeFor(ctx, "t1", 30*24*time.Hour, now)
	if err != nil {
		t.Fatalf("UptimeFor() error = %v", err)
	}
	if math.Abs(up.Percent()-100) > 0.01 {
		t.Errorf("Percent() = %.4f, want 100", up.Percent())
	}
	if up.Window != 2*time.Hour {
		t.Errorf("Window = %s, want 2h (clamped to the first observation)", up.Window)
	}
}

func TestUptimeForWithoutHistory(t *testing.T) {
	s := newTestStore(t)

	up, err := s.UptimeFor(t.Context(), "unknown", 24*time.Hour, time.Now())
	if err != nil {
		t.Fatalf("UptimeFor() error = %v", err)
	}
	if up.Observed {
		t.Error("Observed = true for a tunnel without history, want false")
	}
}

func TestEventsProbesAndPollRuns(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	events := []Event{
		{Timestamp: now.Add(-time.Minute), Kind: EventTunnelStatus, TunnelID: "t1", FromState: "healthy", ToState: "down"},
		{Timestamp: now, Kind: EventConnectorAdded, TunnelID: "t1", Message: "connector c1 appeared"},
	}
	if err := s.AddEvents(ctx, events); err != nil {
		t.Fatalf("AddEvents() error = %v", err)
	}

	got, err := s.Events(ctx, 10)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(got))
	}
	if got[0].Kind != EventConnectorAdded {
		t.Errorf("events[0].Kind = %q, want the newest entry first", got[0].Kind)
	}

	probes := []ProbeResult{
		{Hostname: "a.example.com", CheckedAt: now.Add(-time.Hour), Class: "origin_error", StatusCode: 502},
		{Hostname: "a.example.com", CheckedAt: now, Class: "ok", StatusCode: 200, LatencyMS: 42},
	}
	if err := s.AddProbeResults(ctx, probes); err != nil {
		t.Fatalf("AddProbeResults() error = %v", err)
	}
	latest, err := s.LatestProbeResults(ctx)
	if err != nil {
		t.Fatalf("LatestProbeResults() error = %v", err)
	}
	if latest["a.example.com"].Class != "ok" || latest["a.example.com"].LatencyMS != 42 {
		t.Errorf("latest = %+v, want the newest probe", latest["a.example.com"])
	}

	if err := s.AddPollRun(ctx, PollRun{StartedAt: now, DurationMS: 120, OK: true}); err != nil {
		t.Fatalf("AddPollRun() error = %v", err)
	}
	run, ok, err := s.LastPollRun(ctx)
	if err != nil || !ok {
		t.Fatalf("LastPollRun() = %v, %v, %v", run, ok, err)
	}
	if !run.OK || run.DurationMS != 120 {
		t.Errorf("run = %+v, want a successful 120ms run", run)
	}
}

func TestLastPollRunWhenEmpty(t *testing.T) {
	s := newTestStore(t)
	if _, ok, err := s.LastPollRun(t.Context()); err != nil || ok {
		t.Errorf("LastPollRun() = %v, %v, want false and no error", ok, err)
	}
}

func TestPrune(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-100 * 24 * time.Hour)

	if err := s.AddEvents(ctx, []Event{
		{Timestamp: old, Kind: EventTunnelStatus},
		{Timestamp: now, Kind: EventTunnelStatus},
	}); err != nil {
		t.Fatalf("AddEvents() error = %v", err)
	}
	if err := s.AddProbeResults(ctx, []ProbeResult{{Hostname: "a", CheckedAt: old, Class: "ok"}}); err != nil {
		t.Fatalf("AddProbeResults() error = %v", err)
	}

	if err := s.Prune(ctx, now.Add(-90*24*time.Hour)); err != nil {
		t.Fatalf("Prune() error = %v", err)
	}

	events, err := s.Events(ctx, 10)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(events) != 1 {
		t.Errorf("len(events) = %d, want 1 after pruning", len(events))
	}
	probes, err := s.LatestProbeResults(ctx)
	if err != nil {
		t.Fatalf("LatestProbeResults() error = %v", err)
	}
	if len(probes) != 0 {
		t.Errorf("probes = %+v, want none after pruning", probes)
	}
}

func TestMetaRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	value, at, err := s.GetMeta(ctx, "missing")
	if err != nil || value != "" || !at.IsZero() {
		t.Fatalf("GetMeta(missing) = %q, %v, %v, want empty without error", value, at, err)
	}

	if err := s.SetMeta(ctx, "cloudflared_latest", "2026.4.0", now); err != nil {
		t.Fatalf("SetMeta() error = %v", err)
	}
	if err := s.SetMeta(ctx, "cloudflared_latest", "2026.5.0", now.Add(time.Hour)); err != nil {
		t.Fatalf("SetMeta() overwrite error = %v", err)
	}

	value, at, err = s.GetMeta(ctx, "cloudflared_latest")
	if err != nil {
		t.Fatalf("GetMeta() error = %v", err)
	}
	if value != "2026.5.0" {
		t.Errorf("value = %q, want \"2026.5.0\"", value)
	}
	if !at.Equal(now.Add(time.Hour)) {
		t.Errorf("updatedAt = %v, want %v", at, now.Add(time.Hour))
	}
}

func TestAccessInventoryRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	apps := []AccessApp{{
		ID:          "app1",
		Name:        "app.example.com",
		Domains:     []string{"app.example.com"},
		Type:        "self_hosted",
		HasToken:    true,
		PolicyCount: 2,
	}}
	if err := s.ReplaceAccessApps(ctx, apps, now); err != nil {
		t.Fatalf("ReplaceAccessApps() error = %v", err)
	}
	stored, err := s.AccessApps(ctx)
	if err != nil {
		t.Fatalf("AccessApps() error = %v", err)
	}
	if len(stored) != 1 || !stored[0].HasToken || stored[0].PolicyCount != 2 {
		t.Fatalf("stored = %+v, want the policy summary preserved", stored)
	}
	if len(stored[0].Domains) != 1 || stored[0].Domains[0] != "app.example.com" {
		t.Errorf("domains = %v, want the hostname preserved", stored[0].Domains)
	}

	tokens := []ServiceToken{{ID: "tok1", Name: "cftm-monitor", ExpiresAt: now.Add(24 * time.Hour)}}
	if err := s.ReplaceServiceTokens(ctx, tokens, now); err != nil {
		t.Fatalf("ReplaceServiceTokens() error = %v", err)
	}
	gotTokens, err := s.ServiceTokens(ctx)
	if err != nil {
		t.Fatalf("ServiceTokens() error = %v", err)
	}
	if len(gotTokens) != 1 || gotTokens[0].Name != "cftm-monitor" {
		t.Errorf("tokens = %+v, want cftm-monitor", gotTokens)
	}
}
