package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daknoblo/CFTM/internal/collector"
	"github.com/daknoblo/CFTM/internal/store"
	"github.com/daknoblo/CFTM/internal/web"
)

func postForm(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// A policy the operator just removed must stop raising findings on refresh,
// rather than lingering until the next scheduled Access audit.
func TestRefreshReRunsTheAccessAudit(t *testing.T) {
	var bypassActive atomic.Bool
	bypassActive.Store(true)

	mux := http.NewServeMux()
	mux.Handle("/", cloudflareStub())
	mux.HandleFunc("GET /accounts/acct/access/apps/{id}/policies", func(w http.ResponseWriter, _ *http.Request) {
		if bypassActive.Load() {
			_, _ = w.Write([]byte(`{"success":true,"result":[
			  {"id":"p1","name":"DE only","decision":"bypass","include":[{"geo":{"country_code":"DE"}}]}]}`))
			return
		}
		_, _ = w.Write([]byte(accessPolicyJSON))
	})

	srv, h := newTestServerWith(t, mux)

	if !hasBypassFinding(t, srv) {
		t.Fatal("no bypass finding while the policy is active")
	}

	bypassActive.Store(false)
	if rec := postForm(t, h, "/refresh", url.Values{}); rec.Code != http.StatusOK {
		t.Fatalf("POST /refresh status = %d, want %d", rec.Code, http.StatusOK)
	}

	if hasBypassFinding(t, srv) {
		t.Error("bypass finding still raised after the policy was removed and refreshed")
	}
}

func hasBypassFinding(t *testing.T, srv *Server) bool {
	t.Helper()
	detail, found, err := srv.TunnelDetail(t.Context(), "t1")
	if err != nil || !found {
		t.Fatalf("TunnelDetail() error = %v, found = %v", err, found)
	}
	for _, f := range detail.Card.Findings {
		if f.Code == collector.FindingAccessBypass {
			return true
		}
	}
	return false
}

func TestFindingCanBeMutedAndRestored(t *testing.T) {
	srv, h := newTestServer(t)
	ctx := t.Context()

	detail, found, err := srv.TunnelDetail(ctx, "t1")
	if err != nil {
		t.Fatalf("TunnelDetail() error = %v", err)
	}
	if !found || len(detail.Card.Findings) == 0 {
		t.Fatalf("TunnelDetail() findings = %d, want at least one to mute", len(detail.Card.Findings))
	}
	target := detail.Card.Findings[0]
	before := len(detail.Card.Findings)

	form := url.Values{
		"code":     {target.Code},
		"tunnel":   {"t1"},
		"hostname": {target.Hostname},
		"message":  {target.Message},
	}

	if rec := postForm(t, h, "/findings/ignore", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /findings/ignore = %d, want %d", rec.Code, http.StatusSeeOther)
	}

	detail, _, err = srv.TunnelDetail(ctx, "t1")
	if err != nil {
		t.Fatalf("TunnelDetail() after mute error = %v", err)
	}
	if got := len(detail.Card.Findings); got != before-1 {
		t.Errorf("findings after mute = %d, want %d", got, before-1)
	}
	if got := len(detail.Ignored); got != 1 {
		t.Errorf("muted findings = %d, want 1", got)
	}

	if rec := postForm(t, h, "/findings/restore", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /findings/restore = %d, want %d", rec.Code, http.StatusSeeOther)
	}

	detail, _, err = srv.TunnelDetail(ctx, "t1")
	if err != nil {
		t.Fatalf("TunnelDetail() after restore error = %v", err)
	}
	if got := len(detail.Card.Findings); got != before {
		t.Errorf("findings after restore = %d, want %d", got, before)
	}
	if got := len(detail.Ignored); got != 0 {
		t.Errorf("muted findings after restore = %d, want 0", got)
	}
}

func TestMuteRequiresAFindingCode(t *testing.T) {
	_, h := newTestServer(t)

	rec := postForm(t, h, "/findings/ignore", url.Values{"tunnel": {"t1"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST /findings/ignore without a code = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHostnamesRenderAsSafeExternalLinks(t *testing.T) {
	_, h := newTestServer(t)

	for _, path := range []string{"/ingress", "/audit"} {
		body := get(t, h, path).Body.String()
		want := `<a href="https://app.example.com" target="_blank" rel="noopener noreferrer"`
		if path == "/audit" {
			want = `<a href="https://public-app.example.com" target="_blank" rel="noopener noreferrer"`
		}
		if !strings.Contains(body, want) {
			t.Errorf("%s does not render a hostname link with target and rel, want %s", path, want)
		}
	}
}

// newProbeServer builds a server whose probing is configured with clientID.
func newProbeServer(t *testing.T, clientID string) *Server {
	t.Helper()

	srv, _ := newTestServer(t)
	srv.cfg.ProbeEnabled = true
	srv.cfg.ProbeToken = clientID != ""
	srv.cfg.ProbeClientID = clientID
	return srv
}

func TestAuditPageMarksTheTokenUsedForProbing(t *testing.T) {
	// The stub account owns exactly one token, abc.access.
	page, err := newProbeServer(t, "abc.access").AuditPage(t.Context())
	if err != nil {
		t.Fatalf("AuditPage() error = %v", err)
	}
	if !page.ProbeTokenKnown {
		t.Error("ProbeTokenKnown = false, want true for a client ID the account owns")
	}
	found := false
	for _, token := range page.Tokens {
		if token.ClientID == "abc.access" {
			found = token.InUse
		}
	}
	if !found {
		t.Error("the matching token is not marked InUse")
	}
}

func TestAuditPageFlagsAnUnknownProbeToken(t *testing.T) {
	page, err := newProbeServer(t, "rotated-into-a-new-token.access").AuditPage(t.Context())
	if err != nil {
		t.Fatalf("AuditPage() error = %v", err)
	}
	if !page.ProbeTokenConfigured {
		t.Fatal("ProbeTokenConfigured = false, want true")
	}
	if page.ProbeTokenKnown {
		t.Error("ProbeTokenKnown = true, want false for a client ID no token matches")
	}
	for _, token := range page.Tokens {
		if token.InUse {
			t.Errorf("token %q is marked InUse but does not match the configured client ID", token.ClientID)
		}
	}
}

func TestDashboardSummarizesProbeResults(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := t.Context()

	// t1 serves two HTTP hostnames plus an SSH one and the catch-all, which are
	// not probeable and must stay out of the total.
	err := srv.store.AddProbeResults(ctx, []store.ProbeResult{
		{Hostname: "app.example.com", CheckedAt: time.Now(), Class: "ok", StatusCode: 200},
		{Hostname: "public-app.example.com", CheckedAt: time.Now(), Class: "origin_error", StatusCode: 502},
	})
	if err != nil {
		t.Fatalf("AddProbeResults() error = %v", err)
	}

	d, err := srv.Dashboard(ctx)
	if err != nil {
		t.Fatalf("Dashboard() error = %v", err)
	}
	if len(d.Tunnels) == 0 {
		t.Fatal("Dashboard() returned no tunnels")
	}

	got := d.Tunnels[0].Probes
	want := web.Probes{Total: 2, Checked: 2, OK: 1, Worst: "origin_error"}
	if got != want {
		t.Errorf("Probes = %+v, want %+v", got, want)
	}
	if label := web.ProbeSummaryLabel(got); label != "1 / 2 ok" {
		t.Errorf("ProbeSummaryLabel() = %q, want \"1 / 2 ok\"", label)
	}
}

func permissionByName(t *testing.T, perms []web.Permission, name string) web.Permission {
	t.Helper()
	for _, p := range perms {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no permission row named %q", name)
	return web.Permission{}
}

func TestPermissionsReportWhatTheTokenCouldRead(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.cfg.AuditEnabled = true

	perms := srv.permissions(t.Context())

	// The stub answers the tunnel and Access endpoints, so those are readable.
	if got := permissionByName(t, perms, "Tunnels, connectors and ingress"); got.State != "ok" {
		t.Errorf("tunnels state = %q, want ok", got.State)
	}
	// It serves no alerting endpoint, so that call was refused.
	notifications := permissionByName(t, perms, "Notification policies")
	if notifications.State == "ok" {
		t.Error("notification policies should not report readable against a stub that does not serve them")
	}
	if notifications.Required != "Account : Notifications : Read" {
		t.Errorf("Required = %q, want the Notifications permission", notifications.Required)
	}
}

func TestSwitchedOffAreasAreNotBlamedOnTheToken(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.cfg.AuditEnabled = false
	srv.cfg.AccessLoginsEnabled = false

	perms := srv.permissions(t.Context())

	for _, name := range []string{"Access applications and policies", "Access authentication log"} {
		got := permissionByName(t, perms, name)
		if got.State != "disabled" {
			t.Errorf("%s state = %q, want disabled when the feature is switched off", name, got.State)
		}
	}
}
