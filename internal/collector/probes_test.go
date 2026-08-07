package collector

import (
	"context"
	"testing"
	"time"

	"github.com/daknoblo/CFTM/internal/prober"
	"github.com/daknoblo/CFTM/internal/store"
)

type stubProber struct {
	class  string
	token  bool
	probed []prober.Target
}

func (s *stubProber) HasServiceToken() bool { return s.token }

func (s *stubProber) ProbeAll(_ context.Context, targets []prober.Target) []store.ProbeResult {
	s.probed = targets
	out := make([]store.ProbeResult, 0, len(targets))
	for _, target := range targets {
		out = append(out, store.ProbeResult{
			Hostname:   target.Hostname,
			CheckedAt:  time.Now(),
			Class:      s.class,
			StatusCode: 200,
			LatencyMS:  12,
		})
	}
	return out
}

func (s *stubProber) hostnames() []string {
	out := make([]string, 0, len(s.probed))
	for _, target := range s.probed {
		out = append(out, target.Hostname)
	}
	return out
}

func TestProbeOnceStoresResultsAndTransitions(t *testing.T) {
	api := newFakeAPI()
	api.tunnels.Store(oneHealthyTunnel)
	api.connectors.Store(oneConnector)
	api.configuration.Store(`{"success":true,"result":{"version":3,"config":{"ingress":[
	  {"hostname":"app.example.com","service":"http://127.0.0.1:8091"},
	  {"hostname":"ssh.example.com","service":"ssh://localhost:22"},
	  {"service":"http_status:404"}
	]}}}`)

	c, st := newTestCollector(t, api)
	ctx := t.Context()
	c.PollOnce(ctx)

	p := &stubProber{class: prober.ClassOK, token: true}
	if err := c.ProbeOnce(ctx, p); err != nil {
		t.Fatalf("ProbeOnce() error = %v, want nil", err)
	}

	// Only the HTTP rule is probeable.
	if got := p.hostnames(); len(got) != 1 || got[0] != "app.example.com" {
		t.Fatalf("probed = %v, want only the HTTP hostname", got)
	}

	latest, err := st.LatestProbeResults(ctx)
	if err != nil {
		t.Fatalf("LatestProbeResults() error = %v", err)
	}
	if latest["app.example.com"].Class != prober.ClassOK {
		t.Errorf("stored class = %q, want %q", latest["app.example.com"].Class, prober.ClassOK)
	}

	events := probeEvents(t, st)
	if len(events) != 1 || events[0].ToState != prober.ClassOK {
		t.Fatalf("events = %+v, want one transition into ok", events)
	}
	if events[0].TunnelID != "t1" {
		t.Errorf("event tunnel = %q, want t1", events[0].TunnelID)
	}

	// An unchanged class must not produce another event.
	if err := c.ProbeOnce(ctx, p); err != nil {
		t.Fatalf("second ProbeOnce() error = %v", err)
	}
	if events := probeEvents(t, st); len(events) != 1 {
		t.Errorf("events = %d, want no event for an unchanged class", len(events))
	}

	// A class change is recorded with both states.
	p.class = prober.ClassTunnelDown
	if err := c.ProbeOnce(ctx, p); err != nil {
		t.Fatalf("third ProbeOnce() error = %v", err)
	}
	events = probeEvents(t, st)
	if len(events) != 2 {
		t.Fatalf("events = %d, want a transition to be recorded", len(events))
	}
	if events[0].FromState != prober.ClassOK || events[0].ToState != prober.ClassTunnelDown {
		t.Errorf("event = %+v, want ok -> tunnel_down", events[0])
	}
}

func TestProbeOnceWithoutIngress(t *testing.T) {
	c, _ := newTestCollector(t, newFakeAPI())
	p := &stubProber{class: prober.ClassOK}

	if err := c.ProbeOnce(t.Context(), p); err != nil {
		t.Fatalf("ProbeOnce() error = %v, want nil", err)
	}
	if p.probed != nil {
		t.Errorf("probed = %v, want no probes without ingress rules", p.probed)
	}
}

// The audit is what makes a hostname known to be public; before it has run,
// every target keeps the token so guarded hostnames are still reachable.
func TestProbeOnceWithholdsTokenFromPublicHostnames(t *testing.T) {
	api := newFakeAPI()
	api.tunnels.Store(oneHealthyTunnel)
	api.connectors.Store(oneConnector)
	api.configuration.Store(`{"success":true,"result":{"version":3,"config":{"ingress":[
	  {"hostname":"app.example.com","service":"http://127.0.0.1:8091"},
	  {"hostname":"public-app.example.com","service":"http://127.0.0.1:8096"}
	]}}}`)
	api.accessApps.Store(`{"success":true,"result":[
	  {"id":"a1","name":"app.example.com","domain":"app.example.com"}]}`)

	c, _ := newTestCollector(t, api)
	ctx := t.Context()
	c.PollOnce(ctx)

	p := &stubProber{class: prober.ClassOK, token: true}

	// Without an audit nothing is known to be public, so both carry the token.
	if err := c.ProbeOnce(ctx, p); err != nil {
		t.Fatalf("ProbeOnce() error = %v", err)
	}
	for _, target := range p.probed {
		if !target.UseServiceToken {
			t.Errorf("%s: UseServiceToken = false before the audit ran, want true", target.Hostname)
		}
	}

	if err := c.AuditAccess(ctx); err != nil {
		t.Fatalf("AuditAccess() error = %v", err)
	}
	if err := c.ProbeOnce(ctx, p); err != nil {
		t.Fatalf("ProbeOnce() error = %v", err)
	}

	byHost := map[string]prober.Target{}
	for _, target := range p.probed {
		byHost[target.Hostname] = target
	}
	if len(byHost) != 2 {
		t.Fatalf("probed = %v, want both hostnames", p.probed)
	}
	if !byHost["app.example.com"].UseServiceToken {
		t.Error("guarded hostname should keep the service token")
	}
	if byHost["public-app.example.com"].UseServiceToken {
		t.Error("public hostname must not receive the service token")
	}
}

func probeEvents(t *testing.T, st *store.Store) []store.Event {
	t.Helper()
	all, err := st.Events(t.Context(), 100)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	var out []store.Event
	for _, e := range all {
		if e.Kind == store.EventProbe {
			out = append(out, e)
		}
	}
	return out
}
