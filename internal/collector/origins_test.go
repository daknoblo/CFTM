package collector

import (
	"testing"
	"time"

	"github.com/daknoblo/CFTM/internal/cloudflare"
	"github.com/daknoblo/CFTM/internal/store"
)

func TestServedHostnamesSkipsNonHTTP(t *testing.T) {
	rules := []store.IngressRule{
		{Hostname: "App.example.com", Service: "http://127.0.0.1:8091"},
		{Hostname: "ssh.example.com", Service: "ssh://localhost:22"},
		{Hostname: "", Service: "http_status:404"},
	}
	got := servedHostnames(rules)

	if len(got) != 1 || !got["app.example.com"] {
		t.Errorf("servedHostnames() = %v, want only the lowercased HTTP hostname", got)
	}
}

// The zone answers for everything published under it, so an account zone that
// carries none of our hostnames must not cost an analytics query.
func TestZonesServingSkipsUnrelatedZones(t *testing.T) {
	zones := []cloudflare.Zone{
		{ID: "z1", Name: "example.com"},
		{ID: "z2", Name: "unrelated.org"},
	}
	got := zonesServing(zones, map[string]bool{"app.example.com": true})

	if len(got) != 1 || got[0].ID != "z1" {
		t.Errorf("zonesServing() = %+v, want only z1", got)
	}
}

func TestToHTTPOriginsFiltersAndSplits(t *testing.T) {
	served := map[string]bool{"app.example.com": true}
	from := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)

	groups := []cloudflare.OriginGroup{
		{Country: "DE", Hostname: "app.example.com", Path: "/", Status: 200, Requests: 12},
		{Country: "US", Hostname: "app.example.com", Path: "/", Status: 403, Requests: 4},
		{Country: "FR", Hostname: "app.example.com", Path: "/", Status: 302, Requests: 6},
		{Country: "CN", Hostname: "other.example.com", Path: "/", Status: 200, Requests: 99},
	}
	got := toHTTPOrigins(groups, served, from, to)

	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want the unserved hostname dropped", len(got))
	}
	if got[0].Allowed != 12 || got[0].Denied != 0 {
		t.Errorf("200 bucket = %+v, want it counted as allowed", got[0])
	}
	if got[1].Denied != 4 || got[1].Allowed != 0 {
		t.Errorf("403 bucket = %+v, want it counted as denied", got[1])
	}
	// A 302 is indistinguishable from an ordinary redirect, so it must not be
	// claimed as a refusal.
	if got[2].Denied != 0 || got[2].Allowed != 6 {
		t.Errorf("302 bucket = %+v, want it left as allowed", got[2])
	}
	if got[0].Source != store.OriginSourceHTTP || !got[0].Window.Equal(from) {
		t.Errorf("bucket = %+v, want the source and window recorded", got[0])
	}
}

func TestNormalizeCountry(t *testing.T) {
	cases := map[string]string{
		"de":   "DE",
		"DE":   "DE",
		" us ": "US",
		"":     store.UnknownCountry,
		"XX":   store.UnknownCountry,
		// T1 is what the edge reports for traffic it could not place.
		"T1": store.UnknownCountry,
	}
	for in, want := range cases {
		if got := normalizeCountry(in); got != want {
			t.Errorf("normalizeCountry(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewCountryEvents(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	origins := []store.RequestOrigin{
		{Country: "DE", Hostname: "app.example.com"},
		{Country: "US", Hostname: "app.example.com"},
		{Country: "FR", Hostname: "app.example.com"},
		{Country: "FR", Hostname: "other.example.com"},
		{Country: store.UnknownCountry, Hostname: "app.example.com"},
	}

	c := &Collector{cfg: Config{ExpectedCountries: map[string]bool{"de": true}}}
	events := c.newCountryEvents(origins, map[string]bool{"US": true}, now)

	if len(events) != 1 {
		t.Fatalf("events = %+v, want only the one new unexpected country", events)
	}
	if events[0].ToState != "FR" {
		t.Errorf("event country = %q, want FR", events[0].ToState)
	}
	if events[0].Kind != store.EventOriginCountry {
		t.Errorf("event kind = %q, want %q", events[0].Kind, store.EventOriginCountry)
	}
}

// A country that is already stored is not news, so a restart must not replay
// the whole list as fresh events.
func TestNewCountryEventsStayQuietForKnownCountries(t *testing.T) {
	c := &Collector{}
	origins := []store.RequestOrigin{{Country: "JP", Hostname: "app.example.com"}}

	events := c.newCountryEvents(origins, map[string]bool{"JP": true}, time.Now())
	if len(events) != 0 {
		t.Errorf("events = %+v, want none for an already known country", events)
	}
}

func TestOriginEventSeverityStaysBelowTheNotifyThreshold(t *testing.T) {
	got := EventSeverity(store.Event{Kind: store.EventOriginCountry})
	if got != SeverityInfo {
		t.Errorf("EventSeverity() = %q, want %q so a new country does not page anyone",
			got, SeverityInfo)
	}
}
