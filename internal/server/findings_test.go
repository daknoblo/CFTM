package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
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
