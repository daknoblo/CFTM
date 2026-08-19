package cloudflare

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestGraphQLSendsAPostWithVariables(t *testing.T) {
	var (
		gotMethod string
		gotType   string
		gotBody   graphQLRequest
	)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{"data":{"viewer":{"zones":[{"httpRequestsAdaptiveGroups":[
		  {"count":7,"avg":{"sampleInterval":1},
		   "dimensions":{"clientCountryName":"DE","clientRequestHTTPHost":"a.example.com",
		                 "clientRequestPath":"/api","edgeResponseStatus":200}}]}]}}}`))
	})

	since := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	groups, err := c.HTTPRequestsByCountry(t.Context(), "zone1", since, since.Add(time.Hour))
	if err != nil {
		t.Fatalf("HTTPRequestsByCountry() error = %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want %q", gotMethod, http.MethodPost)
	}
	if gotType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotType)
	}
	// The zone tag must travel as a variable, never spliced into the query.
	if gotBody.Variables["zoneTag"] != "zone1" {
		t.Errorf("variables[zoneTag] = %v, want zone1", gotBody.Variables["zoneTag"])
	}
	if gotBody.Variables["start"] != "2026-08-18T10:00:00Z" {
		t.Errorf("variables[start] = %v, want 2026-08-18T10:00:00Z", gotBody.Variables["start"])
	}

	if len(groups) != 1 {
		t.Fatalf("len(groups) = %d, want 1", len(groups))
	}
	want := OriginGroup{Country: "DE", Hostname: "a.example.com", Path: "/api", Status: 200, Requests: 7}
	if groups[0] != want {
		t.Errorf("group = %+v, want %+v", groups[0], want)
	}
}

// Cloudflare reports a refused query with HTTP 200 and an errors array, so the
// status code alone would read as success.
func TestGraphQLErrorsArriveWithStatusOK(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":null,"errors":[
		  {"message":"not authorized for that account",
		   "extensions":{"code":"authz","ray_id":"abc"}}]}`))
	})

	_, err := c.AccessLoginsByCountry(t.Context(), time.Now().Add(-time.Hour), time.Now())
	if err == nil {
		t.Fatal("AccessLoginsByCountry() error = nil, want the errors array to surface")
	}

	var gqlErr *GraphQLError
	if !errors.As(err, &gqlErr) {
		t.Fatalf("error type = %T, want *GraphQLError", err)
	}
	if !gqlErr.IsAuth() {
		t.Error("IsAuth() = false, want true for extensions.code authz")
	}
}

func TestGraphQLNonAuthErrorIsNotReportedAsPermission(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":null,"errors":[
		  {"message":"cannot request data older than 259200"}]}`))
	})

	_, err := c.HTTPRequestsByCountry(t.Context(), "zone1", time.Now().Add(-time.Hour), time.Now())
	var gqlErr *GraphQLError
	if !errors.As(err, &gqlErr) {
		t.Fatalf("error type = %T, want *GraphQLError", err)
	}
	if gqlErr.IsAuth() {
		t.Error("IsAuth() = true, want false without extensions.code authz")
	}
}

func TestAccessLoginsByCountryReadsTheSuccessFlag(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"viewer":{"accounts":[{"accessLoginRequestsAdaptiveGroups":[
		  {"count":3,"dimensions":{"country":"US","isSuccessfulLogin":0}},
		  {"count":9,"dimensions":{"country":"DE","isSuccessfulLogin":1}}]}]}}}`))
	})

	groups, err := c.AccessLoginsByCountry(t.Context(), time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatalf("AccessLoginsByCountry() error = %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("len(groups) = %d, want 2", len(groups))
	}
	if groups[0].Succeeded || groups[0].Country != "US" || groups[0].Attempts != 3 {
		t.Errorf("groups[0] = %+v, want a denied US bucket of 3", groups[0])
	}
	if !groups[1].Succeeded {
		t.Errorf("groups[1] = %+v, want isSuccessfulLogin 1 to read as succeeded", groups[1])
	}
}

func TestHTTPRequestsSampledFlag(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"viewer":{"zones":[{"httpRequestsAdaptiveGroups":[
		  {"count":40,"avg":{"sampleInterval":10},
		   "dimensions":{"clientCountryName":"FR","clientRequestHTTPHost":"a.example.com",
		                 "clientRequestPath":"/","edgeResponseStatus":302}}]}]}}}`))
	})

	groups, err := c.HTTPRequestsByCountry(t.Context(), "zone1", time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatalf("HTTPRequestsByCountry() error = %v", err)
	}
	if !groups[0].Sampled {
		t.Error("Sampled = false, want true for a sample interval above one")
	}
	// The count stays the raw figure; scaling it would invent precision.
	if groups[0].Requests != 40 {
		t.Errorf("Requests = %d, want the unscaled count 40", groups[0].Requests)
	}
}

func TestHTTPRequestsLimits(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"viewer":{"zones":[{"settings":
		  {"httpRequestsAdaptiveGroups":{"maxDuration":259200,"maxPageSize":10000,"notOlderThan":259200}}}]}}}`))
	})

	limits, err := c.HTTPRequestsLimits(t.Context(), "zone1")
	if err != nil {
		t.Fatalf("HTTPRequestsLimits() error = %v", err)
	}
	if limits.NotOlderThan != 72*time.Hour {
		t.Errorf("NotOlderThan = %v, want 72h", limits.NotOlderThan)
	}
	if !limits.Known() {
		t.Error("Known() = false, want true once the API supplied bounds")
	}
}

func TestClampWindow(t *testing.T) {
	cases := []struct {
		name   string
		limits AnalyticsLimits
		in     time.Duration
		want   time.Duration
	}{
		{"unknown limits leave it alone", AnalyticsLimits{}, 24 * time.Hour, 24 * time.Hour},
		{"retention shortens it", AnalyticsLimits{NotOlderThan: 72 * time.Hour}, 30 * 24 * time.Hour, 72 * time.Hour},
		{"query span shortens it", AnalyticsLimits{NotOlderThan: 90 * 24 * time.Hour, MaxDuration: 7 * 24 * time.Hour}, 30 * 24 * time.Hour, 7 * 24 * time.Hour},
		{"a window inside the bounds is kept", AnalyticsLimits{NotOlderThan: 72 * time.Hour}, time.Hour, time.Hour},
	}
	for _, tc := range cases {
		if got := tc.limits.ClampWindow(tc.in); got != tc.want {
			t.Errorf("%s: ClampWindow(%v) = %v, want %v", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestListZonesFiltersByAccount(t *testing.T) {
	var gotQuery string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Encode()
		_, _ = w.Write([]byte(`{"success":true,"result":[
		  {"id":"z1","name":"example.com","plan":{"name":"Free Website"}}]}`))
	})

	zones, err := c.ListZones(t.Context())
	if err != nil {
		t.Fatalf("ListZones() error = %v", err)
	}
	if want := "account.id=acct&page=1&per_page=50&status=active"; gotQuery != want {
		t.Errorf("query = %q, want %q", gotQuery, want)
	}
	if len(zones) != 1 || zones[0].Plan.Name != "Free Website" {
		t.Errorf("zones = %+v, want the plan name preserved", zones)
	}
}

func TestZoneFor(t *testing.T) {
	zones := []Zone{
		{ID: "z1", Name: "example.com"},
		{ID: "z2", Name: "sub.example.com"},
		{ID: "z3", Name: "notexample.com"},
	}
	cases := []struct {
		host string
		want string
	}{
		{"app.sub.example.com", "z2"}, // the longest suffix wins
		{"www.example.com", "z1"},
		{"example.com", "z1"},
		{"App.Example.com", "z1"}, // matching is case-insensitive
		{"example.org", ""},
		{"", ""},
	}
	for _, tc := range cases {
		zone, ok := ZoneFor(zones, tc.host)
		got := ""
		if ok {
			got = zone.ID
		}
		if got != tc.want {
			t.Errorf("ZoneFor(%q) = %q, want %q", tc.host, got, tc.want)
		}
	}
}

// The Analytics API has its own quota, so its headers must not move the REST
// budget guard, and the other way round.
func TestGraphQLQuotaIsTrackedSeparately(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.Header().Set("Ratelimit", `"default";r=7;t=30`)
			_, _ = w.Write([]byte(`{"data":{"viewer":{"zones":[]}}}`))
			return
		}
		w.Header().Set("Ratelimit", `"default";r=900;t=30`)
		_, _ = w.Write([]byte(`{"success":true,"result":[]}`))
	})

	if _, err := c.ListZones(t.Context()); err != nil {
		t.Fatalf("ListZones() error = %v", err)
	}
	if _, err := c.HTTPRequestsByCountry(t.Context(), "z1", time.Now().Add(-time.Hour), time.Now()); err != nil {
		t.Fatalf("HTTPRequestsByCountry() error = %v", err)
	}

	if got := c.RateLimit().Remaining; got != 900 {
		t.Errorf("REST remaining = %d, want 900 untouched by the graphql call", got)
	}
	if got := c.GraphQLRateLimit().Remaining; got != 7 {
		t.Errorf("graphql remaining = %d, want 7", got)
	}
}
