package collector

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daknoblo/CFTM/internal/cloudflare"
	"github.com/daknoblo/CFTM/internal/store"
)

// fakeAPI serves the tunnel and Access endpoints the collector uses.
type fakeAPI struct {
	tunnels       atomic.Value // string
	connectors    atomic.Value // string
	configuration atomic.Value // string
	accessApps    atomic.Value // string
	accessPolicy  atomic.Value // string
	serviceTokens atomic.Value // string
	configCalls   atomic.Int64
	policyCalls   atomic.Int64
	fail          atomic.Bool
	failPolicies  atomic.Bool
}

func newFakeAPI() *fakeAPI {
	f := &fakeAPI{}
	f.tunnels.Store(`{"success":true,"result":[]}`)
	f.connectors.Store(`{"success":true,"result":[]}`)
	f.configuration.Store(`{"success":true,"result":{"version":1,"config":{"ingress":[]}}}`)
	f.accessApps.Store(`{"success":true,"result":[]}`)
	f.accessPolicy.Store(`{"success":true,"result":[]}`)
	f.serviceTokens.Store(`{"success":true,"result":[]}`)
	return f
}

func (f *fakeAPI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /accounts/acct/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		if f.fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(f.tunnels.Load().(string)))
	})
	mux.HandleFunc("GET /accounts/acct/cfd_tunnel/{id}/connections", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(f.connectors.Load().(string)))
	})
	mux.HandleFunc("GET /accounts/acct/cfd_tunnel/{id}/configurations", func(w http.ResponseWriter, r *http.Request) {
		f.configCalls.Add(1)
		_, _ = w.Write([]byte(f.configuration.Load().(string)))
	})
	mux.HandleFunc("GET /accounts/acct/access/apps", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(f.accessApps.Load().(string)))
	})
	mux.HandleFunc("GET /accounts/acct/access/apps/{id}/policies", func(w http.ResponseWriter, r *http.Request) {
		f.policyCalls.Add(1)
		if f.failPolicies.Load() {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(f.accessPolicy.Load().(string)))
	})
	mux.HandleFunc("GET /accounts/acct/access/service_tokens", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(f.serviceTokens.Load().(string)))
	})
	return mux
}

func newTestCollector(t *testing.T, api *fakeAPI) (*Collector, *store.Store) {
	t.Helper()

	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)

	cf := cloudflare.New("acct", "token",
		cloudflare.WithBaseURL(srv.URL),
		cloudflare.WithHTTPClient(srv.Client()),
		cloudflare.WithBackoffBase(time.Millisecond),
	)

	st, err := store.Open(t.TempDir() + "/cftm.db")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	log := slog.New(slog.DiscardHandler)
	c := New(cf, st, Config{
		PollInterval:       time.Second,
		ConfigRefreshEvery: 10,
		RetentionDays:      90,
	}, log)

	return c, st
}

const oneHealthyTunnel = `{"success":true,"result":[{
  "id":"t1","name":"edge.example.com","status":"healthy","tun_type":"cfd_tunnel",
  "config_src":"cloudflare","remote_config":true,
  "created_at":"2025-01-01T00:00:00Z",
  "connections":[
    {"uuid":"c-1","client_id":"conn1","colo_name":"FRA","origin_ip":"10.0.0.1","client_version":"2026.4.0"},
    {"uuid":"c-2","client_id":"conn1","colo_name":"AMS","origin_ip":"10.0.0.1","client_version":"2026.4.0"}
  ]}]}`

const oneConnector = `{"success":true,"result":[{
  "id":"conn1","arch":"linux_amd64","config_version":3,"version":"2026.4.0",
  "features":["ha-origin"],"run_at":"2026-08-01T00:00:00Z"}]}`

const twoIngressRules = `{"success":true,"result":{"version":3,"source":"cloudflare","config":{"ingress":[
  {"hostname":"app.example.com","service":"http://127.0.0.1:8091"},
  {"service":"http_status:404"}
]}}}`

func TestPollOncePersistsSnapshot(t *testing.T) {
	api := newFakeAPI()
	api.tunnels.Store(oneHealthyTunnel)
	api.connectors.Store(oneConnector)
	api.configuration.Store(twoIngressRules)

	c, st := newTestCollector(t, api)
	ctx := t.Context()

	c.PollOnce(ctx)

	if status := c.Status(); status.LastError != "" {
		t.Fatalf("LastError = %q, want empty", status.LastError)
	}

	tunnels, err := st.Tunnels(ctx)
	if err != nil {
		t.Fatalf("Tunnels() error = %v", err)
	}
	if len(tunnels) != 1 || tunnels[0].Name != "edge.example.com" || tunnels[0].Status != "healthy" {
		t.Fatalf("tunnels = %+v, want one healthy edge.example.com", tunnels)
	}

	connectors, err := st.Connectors(ctx, "t1")
	if err != nil {
		t.Fatalf("Connectors() error = %v", err)
	}
	if len(connectors) != 1 || connectors[0].Version != "2026.4.0" || connectors[0].ConfigVersion != 3 {
		t.Fatalf("connectors = %+v, want one 2026.4.0 connector at config 3", connectors)
	}

	conns, err := st.Connections(ctx, "t1")
	if err != nil {
		t.Fatalf("Connections() error = %v", err)
	}
	if len(conns) != 2 {
		t.Fatalf("len(connections) = %d, want 2", len(conns))
	}

	ingress, err := st.Ingress(ctx, "t1")
	if err != nil {
		t.Fatalf("Ingress() error = %v", err)
	}
	if len(ingress) != 2 || ingress[0].Hostname != "app.example.com" {
		t.Fatalf("ingress = %+v, want the two configured rules", ingress)
	}

	events, err := st.Events(ctx, 50)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	kinds := map[string]int{}
	for _, e := range events {
		kinds[e.Kind]++
	}
	if kinds[store.EventTunnelAdded] != 1 {
		t.Errorf("tunnel_added events = %d, want 1", kinds[store.EventTunnelAdded])
	}
	if kinds[store.EventConnectorAdded] != 1 {
		t.Errorf("connector_added events = %d, want 1", kinds[store.EventConnectorAdded])
	}
	// The initial status is already conveyed by tunnel_added.
	if kinds[store.EventTunnelStatus] != 0 {
		t.Errorf("tunnel_status events = %d, want 0 on discovery", kinds[store.EventTunnelStatus])
	}
}

func TestPollOnceEmitsStatusChange(t *testing.T) {
	api := newFakeAPI()
	api.tunnels.Store(oneHealthyTunnel)
	api.connectors.Store(oneConnector)
	api.configuration.Store(twoIngressRules)

	c, st := newTestCollector(t, api)
	ctx := t.Context()

	c.PollOnce(ctx)
	api.tunnels.Store(strings.Replace(oneHealthyTunnel, `"status":"healthy"`, `"status":"down"`, 1))
	c.PollOnce(ctx)

	events, err := st.Events(ctx, 50)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	var found *store.Event
	for i := range events {
		if events[i].Kind == store.EventTunnelStatus {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatal("no tunnel_status event recorded")
	}
	if found.FromState != "healthy" || found.ToState != "down" {
		t.Errorf("event = %+v, want healthy -> down", *found)
	}
}

func TestConfigurationRefetchedOnlyWhenNeeded(t *testing.T) {
	api := newFakeAPI()
	api.tunnels.Store(oneHealthyTunnel)
	api.connectors.Store(oneConnector)
	api.configuration.Store(twoIngressRules)

	c, _ := newTestCollector(t, api)
	ctx := t.Context()

	c.PollOnce(ctx)
	if n := api.configCalls.Load(); n != 1 {
		t.Fatalf("configuration calls after first poll = %d, want 1", n)
	}

	// Nothing changed, so the second cycle must not spend an API request.
	c.PollOnce(ctx)
	if n := api.configCalls.Load(); n != 1 {
		t.Errorf("configuration calls = %d, want the config fetch to be skipped", n)
	}

	// A connector reporting a newer configuration version forces a refetch.
	api.connectors.Store(strings.Replace(oneConnector, `"config_version":3`, `"config_version":4`, 1))
	api.configuration.Store(strings.Replace(twoIngressRules, `"version":3`, `"version":4`, 1))
	c.PollOnce(ctx)
	if n := api.configCalls.Load(); n != 2 {
		t.Errorf("configuration calls = %d, want a refetch after config drift", n)
	}
}

func TestPollFailureIsRecorded(t *testing.T) {
	api := newFakeAPI()
	api.fail.Store(true)

	c, st := newTestCollector(t, api)
	ctx := t.Context()

	c.PollOnce(ctx)

	if status := c.Status(); status.LastError == "" {
		t.Error("LastError = empty, want the API failure to be recorded")
	}

	run, ok, err := st.LastPollRun(ctx)
	if err != nil || !ok {
		t.Fatalf("LastPollRun() = %v, %v, %v", run, ok, err)
	}
	if run.OK {
		t.Error("run.OK = true, want false")
	}

	events, err := st.Events(ctx, 10)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(events) != 1 || events[0].Kind != store.EventPollFailed {
		t.Errorf("events = %+v, want a single poll_failed entry", events)
	}
}

func TestTriggerIsNonBlocking(t *testing.T) {
	api := newFakeAPI()
	c, _ := newTestCollector(t, api)

	// The buffered channel holds one pending request; further calls must not block.
	for range 5 {
		c.Trigger()
	}
}
