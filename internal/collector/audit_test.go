package collector

import (
	"context"
	"errors"
	"testing"
	"time"
)

type stubChecker struct {
	value string
	err   error
}

func (s stubChecker) Latest(context.Context) (string, error) { return s.value, s.err }

func TestAuditAccess(t *testing.T) {
	api := newFakeAPI()
	api.accessApps.Store(`{"success":true,"result":[
	  {"id":"a1","name":"app.example.com","domain":"app.example.com","type":"self_hosted"},
	  {"id":"a2","name":"metrics.example.com","domain":"metrics.example.com","type":"self_hosted"}
	]}`)
	api.accessPolicy.Store(`{"success":true,"result":[
	  {"id":"p1","name":"Policy-Allow","decision":"allow","include":[{"email":{"email":"a@b.c"}}]},
	  {"id":"p2","name":"Policy-Token","decision":"non_identity","include":[{"any_valid_service_token":{}}]}
	]}`)
	api.serviceTokens.Store(`{"success":true,"result":[
	  {"id":"tok1","name":"cftm-monitor","client_id":"abc.access","expires_at":"2027-08-06T00:00:00Z"}
	]}`)

	c, st := newTestCollector(t, api)
	ctx := t.Context()

	if err := c.AuditAccess(ctx); err != nil {
		t.Fatalf("AuditAccess() error = %v, want nil", err)
	}

	apps, err := st.AccessApps(ctx)
	if err != nil {
		t.Fatalf("AccessApps() error = %v", err)
	}
	if len(apps) != 2 {
		t.Fatalf("len(apps) = %d, want 2", len(apps))
	}
	for _, app := range apps {
		if !app.HasToken {
			t.Errorf("app %s HasToken = false, want true", app.Name)
		}
		if app.HasBypass {
			t.Errorf("app %s HasBypass = true, want false", app.Name)
		}
		if app.PolicyCount != 2 {
			t.Errorf("app %s PolicyCount = %d, want 2", app.Name, app.PolicyCount)
		}
	}
	if n := api.policyCalls.Load(); n != 2 {
		t.Errorf("policy calls = %d, want one per application", n)
	}

	tokens, err := st.ServiceTokens(ctx)
	if err != nil {
		t.Fatalf("ServiceTokens() error = %v", err)
	}
	if len(tokens) != 1 || tokens[0].Name != "cftm-monitor" {
		t.Errorf("tokens = %+v, want cftm-monitor", tokens)
	}

	if at := c.AccessAuditAt(ctx); at.IsZero() {
		t.Error("AccessAuditAt() = zero, want the audit timestamp to be recorded")
	}
}

func TestAuditAccessRecordsBypassPolicies(t *testing.T) {
	api := newFakeAPI()
	api.accessApps.Store(`{"success":true,"result":[
	  {"id":"a1","name":"Beszel","domain":"metrics.example.com/api/beszel","type":"self_hosted"}
	]}`)
	api.accessPolicy.Store(`{"success":true,"result":[
	  {"id":"p1","name":"DE only","decision":"bypass","include":[{"geo":{"country_code":"DE"}}]}
	]}`)

	c, st := newTestCollector(t, api)
	ctx := t.Context()

	if err := c.AuditAccess(ctx); err != nil {
		t.Fatalf("AuditAccess() error = %v, want nil", err)
	}

	apps, err := st.AccessApps(ctx)
	if err != nil {
		t.Fatalf("AccessApps() error = %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("len(apps) = %d, want 1", len(apps))
	}
	app := apps[0]
	if !app.HasBypass {
		t.Error("HasBypass = false, want true")
	}
	if len(app.BypassPolicies) != 1 || app.BypassPolicies[0] != "DE only" {
		t.Errorf("BypassPolicies = %v, want [DE only]", app.BypassPolicies)
	}
	if len(app.Domains) != 1 || app.Domains[0] != "metrics.example.com" {
		t.Errorf("Domains = %v, want the path stripped for ingress matching", app.Domains)
	}
	if len(app.RawDomains) != 1 || app.RawDomains[0] != "metrics.example.com/api/beszel" {
		t.Errorf("RawDomains = %v, want the path preserved", app.RawDomains)
	}
}

func TestAuditAccessToleratesUnreadablePolicies(t *testing.T) {
	api := newFakeAPI()
	api.accessApps.Store(`{"success":true,"result":[{"id":"a1","name":"app","domain":"app.example.com"}]}`)
	api.failPolicies.Store(true)

	c, st := newTestCollector(t, api)
	ctx := t.Context()

	err := c.AuditAccess(ctx)
	if err == nil {
		t.Fatal("AuditAccess() error = nil, want the policy failure to be reported")
	}

	// The application inventory must still be persisted so coverage keeps working.
	apps, storeErr := st.AccessApps(ctx)
	if storeErr != nil {
		t.Fatalf("AccessApps() error = %v", storeErr)
	}
	if len(apps) != 1 || apps[0].Domains[0] != "app.example.com" {
		t.Errorf("apps = %+v, want the application to be stored despite the policy error", apps)
	}
}

func TestAccessAuditAtWithoutRun(t *testing.T) {
	c, _ := newTestCollector(t, newFakeAPI())
	if at := c.AccessAuditAt(t.Context()); !at.IsZero() {
		t.Errorf("AccessAuditAt() = %v, want the zero time", at)
	}
}

func TestRunReleaseChecksCachesVersion(t *testing.T) {
	c, _ := newTestCollector(t, newFakeAPI())

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.RunReleaseChecks(ctx, stubChecker{value: "2026.4.0"}, time.Hour)
	}()

	// The first lookup runs before the ticker, so the value is available at once.
	waitFor(t, func() bool { return c.LatestRelease(t.Context()) == "2026.4.0" })

	cancel()
	<-done
}

func TestLatestReleaseWithoutSuccessfulLookup(t *testing.T) {
	c, _ := newTestCollector(t, newFakeAPI())

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.RunReleaseChecks(ctx, stubChecker{err: errors.New("rate limited")}, time.Hour)
	}()
	cancel()
	<-done

	if got := c.LatestRelease(t.Context()); got != "" {
		t.Errorf("LatestRelease() = %q, want empty after a failed lookup", got)
	}
}

// waitFor polls cond until it holds or the test times out.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within the timeout")
}
