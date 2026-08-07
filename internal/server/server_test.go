package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/daknoblo/CFTM/internal/cloudflare"
	"github.com/daknoblo/CFTM/internal/collector"
	"github.com/daknoblo/CFTM/internal/logbuf"
	"github.com/daknoblo/CFTM/internal/store"
	"github.com/daknoblo/CFTM/internal/web"
)

const (
	tunnelsJSON = `{"success":true,"result":[{
	  "id":"t1","name":"edge.example.com","status":"healthy","tun_type":"cfd_tunnel",
	  "config_src":"cloudflare","remote_config":true,"created_at":"2025-01-01T00:00:00Z",
	  "connections":[
	    {"uuid":"c-1","client_id":"conn1","colo_name":"FRA","origin_ip":"10.0.0.1"},
	    {"uuid":"c-2","client_id":"conn1","colo_name":"AMS","origin_ip":"10.0.0.1"}
	  ]}]}`

	connectorsJSON = `{"success":true,"result":[{
	  "id":"conn1","arch":"linux_amd64","config_version":3,"version":"2026.4.0",
	  "features":["ha-origin"],"run_at":"2026-08-01T00:00:00Z"}]}`

	configJSON = `{"success":true,"result":{"version":3,"source":"cloudflare","config":{"ingress":[
	  {"hostname":"app.example.com","service":"http://127.0.0.1:8091"},
	  {"hostname":"public-app.example.com","service":"http://127.0.0.1:8096"},
	  {"hostname":"ssh.example.com","service":"ssh://localhost:22"},
	  {"service":"http_status:404"}
	]}}}`

	accessAppsJSON = `{"success":true,"result":[
	  {"id":"a1","name":"app.example.com","domain":"app.example.com","type":"self_hosted"}]}`

	accessPolicyJSON = `{"success":true,"result":[
	  {"id":"p1","name":"Policy-Token","decision":"non_identity","include":[{"any_valid_service_token":{}}]}]}`

	serviceTokensJSON = `{"success":true,"result":[
	  {"id":"tok1","name":"cftm-monitor","client_id":"abc.access","expires_at":"2026-08-20T00:00:00Z"}]}`
)

func cloudflareStub() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /accounts/acct/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(tunnelsJSON))
	})
	mux.HandleFunc("GET /accounts/acct/cfd_tunnel/{id}/connections", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(connectorsJSON))
	})
	mux.HandleFunc("GET /accounts/acct/cfd_tunnel/{id}/configurations", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(configJSON))
	})
	mux.HandleFunc("GET /accounts/acct/access/apps", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(accessAppsJSON))
	})
	mux.HandleFunc("GET /accounts/acct/access/apps/{id}/policies", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(accessPolicyJSON))
	})
	mux.HandleFunc("GET /accounts/acct/access/service_tokens", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(serviceTokensJSON))
	})
	return mux
}

// sentinelToken is distinctive enough that any leak into a response body is
// unambiguous.
const sentinelToken = "cf-api-token-must-never-be-rendered"

func newTestServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()

	api := httptest.NewServer(cloudflareStub())
	t.Cleanup(api.Close)

	cf := cloudflare.New("acct", sentinelToken,
		cloudflare.WithBaseURL(api.URL),
		cloudflare.WithHTTPClient(api.Client()),
		cloudflare.WithBackoffBase(time.Millisecond),
	)

	st, err := store.Open(t.TempDir() + "/cftm.db")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	log := slog.New(slog.DiscardHandler)
	buf := logbuf.New(50)
	coll := collector.New(cf, st, collector.Config{
		PollInterval:       time.Minute,
		ConfigRefreshEvery: 10,
		RetentionDays:      90,
	}, log)

	coll.PollOnce(t.Context())
	if err := coll.AuditAccess(t.Context()); err != nil {
		t.Fatalf("AuditAccess() error = %v", err)
	}

	srv, err := New(st, coll, nil, buf, Config{
		AccountID:     "acct",
		PollInterval:  30 * time.Second,
		RetentionDays: 90,
		AuditEnabled:  true,
	}, log)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return srv, srv.Handler()
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestPagesRender(t *testing.T) {
	_, h := newTestServer(t)

	cases := []struct {
		path string
		want string
	}{
		{"/", "edge.example.com"},
		{"/tunnels/t1", "Edge connections"},
		{"/ingress", "app.example.com"},
		{"/audit", "Access service tokens"},
		{"/events", "Events"},
		{"/logs", "Logs"},
		{"/about", "Configuration"},
		{"/partials/tunnels", "tunnel-cards"},
		{"/partials/poll-status", "poll-status"},
		{"/partials/events", "event-list"},
		{"/partials/log", "log-lines"},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			rec := get(t, h, tc.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200", tc.path, rec.Code)
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("GET %s body does not contain %q", tc.path, tc.want)
			}
		})
	}
}

func TestHealthz(t *testing.T) {
	_, h := newTestServer(t)

	rec := get(t, h, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != `{"status":"ok"}` {
		t.Errorf("body = %q, want the ok payload", got)
	}
}

func TestUnknownTunnelIs404(t *testing.T) {
	_, h := newTestServer(t)

	if rec := get(t, h, "/tunnels/does-not-exist"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /tunnels/does-not-exist = %d, want 404", rec.Code)
	}
	if rec := get(t, h, "/api/tunnels/does-not-exist"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/tunnels/does-not-exist = %d, want 404", rec.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	_, h := newTestServer(t)
	rec := get(t, h, "/")

	want := map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
		"Referrer-Policy":              "no-referrer",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
	}
	for header, value := range want {
		if got := rec.Header().Get(header); got != value {
			t.Errorf("%s = %q, want %q", header, got, value)
		}
	}

	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("CSP = %q, want script-src 'self'", csp)
	}
	if strings.Contains(csp, "unsafe-inline") {
		t.Errorf("CSP = %q, must not allow unsafe-inline", csp)
	}
}

func TestStaticAssetsUseETag(t *testing.T) {
	_, h := newTestServer(t)

	rec := get(t, h, "/static/app.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /static/app.js = %d, want 200", rec.Code)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("ETag header is empty")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}

	req := httptest.NewRequest(http.MethodGet, "/static/app.js", nil)
	req.Header.Set("If-None-Match", etag)
	cached := httptest.NewRecorder()
	h.ServeHTTP(cached, req)
	if cached.Code != http.StatusNotModified {
		t.Errorf("conditional GET = %d, want 304", cached.Code)
	}
}

func TestStaticUnknownAssetIs404(t *testing.T) {
	_, h := newTestServer(t)
	if rec := get(t, h, "/static/nope.js"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /static/nope.js = %d, want 404", rec.Code)
	}
}

func TestAPITunnels(t *testing.T) {
	_, h := newTestServer(t)

	rec := get(t, h, "/api/tunnels")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/tunnels = %d, want 200", rec.Code)
	}

	var payload struct {
		Tunnels []web.TunnelCard `json:"tunnels"`
		Totals  web.Totals       `json:"totals"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decoding response failed: %v", err)
	}
	if len(payload.Tunnels) != 1 || payload.Tunnels[0].Name != "edge.example.com" {
		t.Fatalf("tunnels = %+v, want edge.example.com", payload.Tunnels)
	}
	if payload.Totals.Hostnames != 3 {
		t.Errorf("Totals.Hostnames = %d, want 3", payload.Totals.Hostnames)
	}
	// public-app.example.com has no Access application in the stub.
	if payload.Totals.Unprotected != 1 {
		t.Errorf("Totals.Unprotected = %d, want 1", payload.Totals.Unprotected)
	}
}

func TestResponsesNeverLeakCredentials(t *testing.T) {
	_, h := newTestServer(t)

	for _, path := range []string{"/api/status", "/api/tunnels", "/about", "/"} {
		t.Run(path, func(t *testing.T) {
			rec := get(t, h, path)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200", path, rec.Code)
			}
			body := rec.Body.String()
			if strings.Contains(body, sentinelToken) {
				t.Errorf("GET %s leaked the API token", path)
			}
			if strings.Contains(body, "Bearer ") {
				t.Errorf("GET %s leaked an Authorization header", path)
			}
		})
	}
}

func TestIngressPageMarksProbeability(t *testing.T) {
	srv, _ := newTestServer(t)

	page, err := srv.IngressPage(t.Context())
	if err != nil {
		t.Fatalf("IngressPage() error = %v", err)
	}
	if len(page.Rules) != 4 {
		t.Fatalf("len(rules) = %d, want 4", len(page.Rules))
	}

	byHost := map[string]web.Ingress{}
	for _, rule := range page.Rules {
		byHost[rule.Hostname] = rule
	}

	if !byHost["app.example.com"].Probeable {
		t.Error("HTTP rule should be probeable")
	}
	if byHost["ssh.example.com"].Probeable {
		t.Error("ssh rule should not be probeable")
	}
	if byHost[""].Kind != "catch-all" {
		t.Errorf("catch-all Kind = %q, want \"catch-all\"", byHost[""].Kind)
	}
	if !byHost["app.example.com"].Access.Protected {
		t.Error("waim-cloud should be marked as Access protected")
	}
	if byHost["public-app.example.com"].Access.Protected {
		t.Error("public-app.example.com has no Access application and must be marked public")
	}
}

func TestAuditPageFlagsExpiringToken(t *testing.T) {
	srv, _ := newTestServer(t)

	page, err := srv.AuditPage(t.Context())
	if err != nil {
		t.Fatalf("AuditPage() error = %v", err)
	}
	if !page.Available {
		t.Fatal("Available = false, want true after a successful audit")
	}
	if len(page.Unprotected) != 1 || page.Unprotected[0].Hostname != "public-app.example.com" {
		t.Errorf("unprotected = %+v, want public-app.example.com", page.Unprotected)
	}
	if len(page.Tokens) != 1 {
		t.Fatalf("len(tokens) = %d, want 1", len(page.Tokens))
	}
	if page.Tokens[0].Severity == "" {
		t.Error("a token expiring within the warning window should carry a severity")
	}
}

// A declared public hostname is still monitored, it just stops counting as a gap.
func TestDeclaredPublicHostnameIsNotAGap(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.cfg.ExpectedPublic = map[string]bool{"public-app.example.com": true}
	ctx := t.Context()

	page, err := srv.AuditPage(ctx)
	if err != nil {
		t.Fatalf("AuditPage() error = %v", err)
	}
	if len(page.Unprotected) != 0 {
		t.Errorf("unprotected = %+v, want none", page.Unprotected)
	}
	if len(page.IntentionallyPublic) != 1 || page.IntentionallyPublic[0].Hostname != "public-app.example.com" {
		t.Fatalf("intentionally public = %+v, want public-app.example.com", page.IntentionallyPublic)
	}

	dash, err := srv.Dashboard(ctx)
	if err != nil {
		t.Fatalf("Dashboard() error = %v", err)
	}
	if dash.Totals.Unprotected != 0 {
		t.Errorf("Totals.Unprotected = %d, want 0", dash.Totals.Unprotected)
	}
	for _, card := range dash.Tunnels {
		for _, f := range card.Findings {
			if f.Hostname == "public-app.example.com" && f.Code == "access_unprotected" {
				t.Error("a declared public hostname should raise no coverage finding")
			}
		}
	}

	// It remains part of the ingress inventory, so it keeps being probed.
	ingress, err := srv.IngressPage(ctx)
	if err != nil {
		t.Fatalf("IngressPage() error = %v", err)
	}
	found := false
	for _, rule := range ingress.Rules {
		if rule.Hostname == "public-app.example.com" {
			found = rule.Probeable
		}
	}
	if !found {
		t.Error("public-app.example.com should still be a probe target")
	}
}

func TestRefreshEndpointReturnsCards(t *testing.T) {
	_, h := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/refresh", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /refresh = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "tunnel-cards") {
		t.Error("response should contain the swapped tunnel cards")
	}
}

func TestRefreshEndpointIsThrottled(t *testing.T) {
	_, h := newTestServer(t)

	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/refresh", nil)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if got := post().Code; got != http.StatusOK {
		t.Fatalf("first POST /refresh = %d, want %d", got, http.StatusOK)
	}

	rec := post()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second POST /refresh = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("a throttled response should carry Retry-After")
	}
}

func TestProbeEndpointDisabled(t *testing.T) {
	_, h := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /probe = %d, want 404 when probing is disabled", rec.Code)
	}
}

func TestCrossOriginPostIsRejected(t *testing.T) {
	_, h := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/refresh", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Error("cross-site POST should be rejected")
	}
}
