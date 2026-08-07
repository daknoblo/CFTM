package cloudflare

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New("acct", "token",
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithBackoffBase(time.Millisecond),
	)
}

func TestListTunnels(t *testing.T) {
	const body = `{
	  "success": true,
	  "result": [
	    {
	      "id": "f70ff985-a4ef-4643-bbbc-4a0ed4fc8415",
	      "account_tag": "699d98642c564d2e855e9661899b7252",
	      "config_src": "cloudflare",
	      "connections": [
	        {
	          "id": "1bedc50d-42b3-473c-b108-ff3d10c0d925",
	          "client_id": "1bedc50d-42b3-473c-b108-ff3d10c0d925",
	          "client_version": "2026.4.0",
	          "colo_name": "FRA",
	          "opened_at": "2021-01-25T18:22:34.317854Z",
	          "origin_ip": "10.1.0.137",
	          "uuid": "1bedc50d-42b3-473c-b108-ff3d10c0d925"
	        }
	      ],
	      "conns_active_at": "2009-11-10T23:00:00Z",
	      "conns_inactive_at": null,
	      "created_at": "2021-01-25T18:22:34.317854Z",
	      "deleted_at": null,
	      "name": "edge.example.com",
	      "remote_config": true,
	      "status": "healthy",
	      "tun_type": "cfd_tunnel"
	    }
	  ],
	  "result_info": {"count": 1, "page": 1, "per_page": 50, "total_count": 1}
	}`

	var gotPath, gotAuth, gotQuery string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Encode()
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})

	tunnels, err := c.ListTunnels(t.Context())
	if err != nil {
		t.Fatalf("ListTunnels() error = %v, want nil", err)
	}
	if len(tunnels) != 1 {
		t.Fatalf("len(tunnels) = %d, want 1", len(tunnels))
	}

	got := tunnels[0]
	if got.Name != "edge.example.com" {
		t.Errorf("Name = %q, want \"edge.example.com\"", got.Name)
	}
	if got.Status != StatusHealthy {
		t.Errorf("Status = %q, want %q", got.Status, StatusHealthy)
	}
	if !got.RemoteConfig || got.ConfigSrc != "cloudflare" {
		t.Errorf("remote config = %v/%q, want true/\"cloudflare\"", got.RemoteConfig, got.ConfigSrc)
	}
	if len(got.Connections) != 1 || got.Connections[0].ColoName != "FRA" {
		t.Errorf("connections = %+v, want one entry in FRA", got.Connections)
	}
	if !got.DeletedAt.IsZero() || !got.ConnsInactiveAt.IsZero() {
		t.Error("null timestamps should decode to the zero time")
	}
	if got.CreatedAt.IsZero() {
		t.Error("created_at should decode to a non-zero time")
	}

	if gotPath != "/accounts/acct/cfd_tunnel" {
		t.Errorf("path = %q, want \"/accounts/acct/cfd_tunnel\"", gotPath)
	}
	if gotAuth != "Bearer token" {
		t.Errorf("Authorization = %q, want \"Bearer token\"", gotAuth)
	}
	if want := "is_deleted=false&page=1&per_page=50"; gotQuery != want {
		t.Errorf("query = %q, want %q", gotQuery, want)
	}
}

func TestListTunnelsPaginates(t *testing.T) {
	var pages atomic.Int64
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		pages.Add(1)
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		w.Header().Set("Content-Type", "application/json")
		if page == 1 {
			// A full page forces the client to ask for the next one.
			w.Write([]byte(`{"success":true,"result":[` + repeatTunnels(perPage) + `],
			  "result_info":{"count":50,"page":1,"per_page":50,"total_count":51}}`))
			return
		}
		w.Write([]byte(`{"success":true,"result":[{"id":"last","name":"n"}],
		  "result_info":{"count":1,"page":2,"per_page":50,"total_count":51}}`))
	})

	tunnels, err := c.ListTunnels(t.Context())
	if err != nil {
		t.Fatalf("ListTunnels() error = %v, want nil", err)
	}
	if len(tunnels) != perPage+1 {
		t.Errorf("len(tunnels) = %d, want %d", len(tunnels), perPage+1)
	}
	if n := pages.Load(); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
}

func repeatTunnels(n int) string {
	out := ""
	for i := range n {
		if i > 0 {
			out += ","
		}
		out += `{"id":"t` + strconv.Itoa(i) + `","name":"n"}`
	}
	return out
}

func TestRetriesServerErrors(t *testing.T) {
	var calls atomic.Int64
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write([]byte(`{"success":true,"result":[]}`))
	})

	if _, err := c.ListTunnels(t.Context()); err != nil {
		t.Fatalf("ListTunnels() error = %v, want nil after retries", err)
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("calls = %d, want 3", n)
	}
}

func TestRateLimitedIsNotRetriedAndBlocksNextCall(t *testing.T) {
	var calls atomic.Int64
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"rate limited"}]}`))
	})

	_, err := c.ListTunnels(t.Context())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !apiErr.IsRateLimited() {
		t.Fatalf("error = %v, want a 429 APIError", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("calls = %d, want 1 (429 must not be retried)", n)
	}

	// The recorded reset window must stop the next call before it is sent.
	_, err = c.ListTunnels(t.Context())
	if !errors.Is(err, ErrBudgetGuard) {
		t.Errorf("second call error = %v, want ErrBudgetGuard", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("calls = %d, want the guard to prevent a second request", n)
	}
}

func TestBudgetGuardOnLowRemaining(t *testing.T) {
	var calls atomic.Int64
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Ratelimit", `"default";r=40;t=60`)
		w.Header().Set("Ratelimit-Policy", `"default";q=1200;w=300`)
		w.Write([]byte(`{"success":true,"result":[]}`))
	})

	if _, err := c.ListTunnels(t.Context()); err != nil {
		t.Fatalf("first call error = %v, want nil", err)
	}
	if rl := c.RateLimit(); rl.Remaining != 40 || rl.Quota != 1200 {
		t.Errorf("RateLimit() = %+v, want remaining 40 and quota 1200", rl)
	}

	if _, err := c.ListTunnels(t.Context()); !errors.Is(err, ErrBudgetGuard) {
		t.Errorf("second call error = %v, want ErrBudgetGuard", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("calls = %d, want 1", n)
	}
}

func TestUnsuccessfulEnvelope(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":false,"errors":[{"code":7003,"message":"Could not route"}],"result":null}`))
	})

	_, err := c.ListTunnels(t.Context())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want an *APIError", err)
	}
	if len(apiErr.Details) != 1 || apiErr.Details[0].Code != 7003 {
		t.Errorf("details = %+v, want the 7003 entry", apiErr.Details)
	}
}

func TestAuthErrorIsNotRetried(t *testing.T) {
	var calls atomic.Int64
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"success":false,"errors":[{"code":9109,"message":"Invalid access token"}]}`))
	})

	_, err := c.ListTunnels(t.Context())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !apiErr.IsAuth() {
		t.Fatalf("error = %v, want an auth APIError", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("calls = %d, want 1", n)
	}
}

func TestGetConfiguration(t *testing.T) {
	const body = `{
	  "success": true,
	  "result": {
	    "account_id": "acct",
	    "tunnel_id": "tid",
	    "version": 7,
	    "source": "cloudflare",
	    "config": {
	      "ingress": [
	        {"hostname": "home.example.com", "service": "http://127.0.0.1:8123"},
	        {"hostname": "camera.example.com", "service": "https://127.0.0.1:8971",
	         "originRequest": {"noTLSVerify": true, "connectTimeout": 10}},
	        {"hostname": "ssh.example.com", "service": "ssh://localhost:22"},
	        {"service": "http_status:404"}
	      ],
	      "warp-routing": {"enabled": false}
	    }
	  }
	}`

	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(body))
	})

	cfg, err := c.GetConfiguration(t.Context(), "tid")
	if err != nil {
		t.Fatalf("GetConfiguration() error = %v, want nil", err)
	}
	if gotPath != "/accounts/acct/cfd_tunnel/tid/configurations" {
		t.Errorf("path = %q", gotPath)
	}
	if cfg.Version != 7 {
		t.Errorf("Version = %d, want 7", cfg.Version)
	}
	if len(cfg.Config.Ingress) != 4 {
		t.Fatalf("len(ingress) = %d, want 4", len(cfg.Config.Ingress))
	}

	rules := cfg.Config.Ingress
	if !rules[0].IsHTTPService() || !rules[1].IsHTTPService() {
		t.Error("http:// and https:// rules should be probe targets")
	}
	if rules[2].IsHTTPService() {
		t.Error("ssh:// rule should not be a probe target")
	}
	if rules[3].IsHTTPService() || !rules[3].IsCatchAll() {
		t.Error("http_status:404 rule should be the catch-all and not a probe target")
	}
	if rules[1].OriginRequest == nil || rules[1].OriginRequest.NoTLSVerify == nil || !*rules[1].OriginRequest.NoTLSVerify {
		t.Error("noTLSVerify should decode to true")
	}
}

func TestListConnectors(t *testing.T) {
	const body = `{
	  "success": true,
	  "result": [
	    {
	      "id": "1bedc50d-42b3-473c-b108-ff3d10c0d925",
	      "arch": "linux_arm64",
	      "config_version": 7,
	      "conns": [{"colo_name": "FRA", "origin_ip": "10.1.0.137"}],
	      "features": ["ha-origin"],
	      "run_at": "2026-08-01T10:00:00Z",
	      "version": "2026.4.0"
	    }
	  ]
	}`

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	})

	connectors, err := c.ListConnectors(t.Context(), "tid")
	if err != nil {
		t.Fatalf("ListConnectors() error = %v, want nil", err)
	}
	if len(connectors) != 1 {
		t.Fatalf("len(connectors) = %d, want 1", len(connectors))
	}
	got := connectors[0]
	if got.Version != "2026.4.0" || got.Arch != "linux_arm64" || got.ConfigVersion != 7 {
		t.Errorf("connector = %+v, want version 2026.4.0 / linux_arm64 / config 7", got)
	}
	if got.RunAt.IsZero() {
		t.Error("run_at should decode")
	}
}

func TestContextCancellationStopsRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var calls atomic.Int64
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		cancel()
		w.WriteHeader(http.StatusInternalServerError)
	})

	if _, err := c.ListTunnels(ctx); err == nil {
		t.Fatal("ListTunnels() error = nil, want a context error")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("calls = %d, want 1", n)
	}
}

func TestParseRateLimitHeader(t *testing.T) {
	cases := []struct {
		in            string
		wantRemaining int
		wantReset     time.Duration
		wantOK        bool
	}{
		{`"default";r=50;t=30`, 50, 30 * time.Second, true},
		{`"burst";r=90;t=10, "default";r=12;t=250`, 12, 250 * time.Second, true},
		{`"default";r=0;t=300`, 0, 300 * time.Second, true},
		{"", 0, 0, false},
		{"garbage", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			remaining, reset, ok := parseRateLimitHeader(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if remaining != tc.wantRemaining {
				t.Errorf("remaining = %d, want %d", remaining, tc.wantRemaining)
			}
			if reset != tc.wantReset {
				t.Errorf("reset = %s, want %s", reset, tc.wantReset)
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("120"); got != 2*time.Minute {
		t.Errorf("parseRetryAfter(\"120\") = %s, want 2m", got)
	}
	if got := parseRetryAfter(""); got != 0 {
		t.Errorf("parseRetryAfter(\"\") = %s, want 0", got)
	}
	if got := parseRetryAfter("not-a-date"); got != 0 {
		t.Errorf("parseRetryAfter(\"not-a-date\") = %s, want 0", got)
	}
}
