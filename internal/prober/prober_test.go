package prober

import (
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daknoblo/CFTM/internal/store"
)

// errString is a minimal error value for classification tests.
type errString string

func (e errString) Error() string { return string(e) }

// timeoutError satisfies net.Error and reports a timeout.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// errorTransport always fails, so error handling can be exercised.
type errorTransport struct{ msg string }

func (t errorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errString(t.msg)
}

func TestTargetsSkipsNonHTTPRules(t *testing.T) {
	rules := []store.IngressRule{
		{Hostname: "app.example.com", Service: "http://127.0.0.1:8091"},
		{Hostname: "camera.example.com", Service: "https://127.0.0.1:8971"},
		{Hostname: "ssh.example.com", Service: "ssh://localhost:22"},
		{Hostname: "", Service: "http_status:404"},
		{Hostname: "*.example.com", Service: "http://127.0.0.1:1"},
		{Hostname: "app.example.com", Service: "http://127.0.0.1:8091"},
	}

	got := Targets(rules, nil)
	want := []string{"app.example.com", "camera.example.com"}
	if len(got) != len(want) {
		t.Fatalf("Targets() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i].Hostname != want[i] {
			t.Errorf("Targets()[%d].Hostname = %q, want %q", i, got[i].Hostname, want[i])
		}
		if !got[i].UseServiceToken {
			t.Errorf("Targets()[%d].UseServiceToken = false, want true without audit data", i)
		}
	}
}

func TestTargetsWithholdsTokenFromPublicHostnames(t *testing.T) {
	rules := []store.IngressRule{
		{Hostname: "app.example.com", Service: "http://127.0.0.1:8091"},
		{Hostname: "public-app.example.com", Service: "http://127.0.0.1:8096"},
	}
	publicHostnames := map[string]bool{"public-app.example.com": true}

	targets := Targets(rules, publicHostnames)
	if len(targets) != 2 {
		t.Fatalf("len(targets) = %d, want 2", len(targets))
	}

	byHost := map[string]Target{}
	for _, target := range targets {
		byHost[target.Hostname] = target
	}
	if !byHost["app.example.com"].UseServiceToken {
		t.Error("a guarded hostname should carry the service token")
	}
	if byHost["public-app.example.com"].UseServiceToken {
		t.Error("a public hostname must not carry the service token")
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		headers map[string]string
		body    string
		want    string
	}{
		{"origin answered", http.StatusOK, nil, "hello", ClassOK},
		{"origin 404 is still a working path", http.StatusNotFound, nil, "", ClassOK},
		{"origin 500 is the app's problem", http.StatusInternalServerError, nil, "", ClassOK},
		{"cached response", http.StatusOK, map[string]string{"CF-Cache-Status": "HIT"}, "", ClassEdgeCached},
		{"stale response", http.StatusOK, map[string]string{"CF-Cache-Status": "STALE"}, "", ClassEdgeCached},
		{"background refresh", http.StatusOK, map[string]string{"CF-Cache-Status": "UPDATING"}, "", ClassEdgeCached},
		{"revalidated cache", http.StatusOK, map[string]string{"CF-Cache-Status": "REVALIDATED"}, "", ClassEdgeCached},
		{"normalized cache header", http.StatusOK, map[string]string{"CF-Cache-Status": " hit "}, "", ClassEdgeCached},
		{"cache miss", http.StatusOK, map[string]string{"CF-Cache-Status": "MISS"}, "", ClassOK},
		{"dynamic response", http.StatusOK, map[string]string{"CF-Cache-Status": "DYNAMIC"}, "", ClassOK},
		{"cache bypass", http.StatusOK, map[string]string{"CF-Cache-Status": "BYPASS"}, "", ClassOK},
		{"expired cache", http.StatusOK, map[string]string{"CF-Cache-Status": "EXPIRED"}, "", ClassOK},
		{
			name:    "access login redirect",
			status:  http.StatusFound,
			headers: map[string]string{"Location": "https://team.cloudflareaccess.com/cdn-cgi/access/login/x"},
			want:    ClassAccessChallenge,
		},
		{
			name:    "ordinary redirect",
			status:  http.StatusFound,
			headers: map[string]string{"Location": "https://app.example.com/login"},
			want:    ClassOK,
		},
		{
			name:    "access denies the token",
			status:  http.StatusForbidden,
			headers: map[string]string{"Cf-Access-Error": "1"},
			want:    ClassAccessDenied,
		},
		{
			name:   "origin 403 is not an access denial",
			status: http.StatusForbidden,
			want:   ClassOK,
		},
		{
			name:   "unauthorized with access marker",
			status: http.StatusUnauthorized,
			body:   `<a href="https://team.cloudflareaccess.com/">login</a>`,
			want:   ClassAccessChallenge,
		},
		{"tunnel has no connector", 530, nil, "error code: 1033", ClassTunnelDown},
		{"bare 530", 530, nil, "", ClassTunnelDown},
		{"origin unreachable", http.StatusBadGateway, nil, "", ClassOriginError},
		{"origin timed out", http.StatusGatewayTimeout, nil, "", ClassOriginError},
		{"connection refused by origin", 521, nil, "", ClassOriginError},
		{"502 carrying 1033", http.StatusBadGateway, nil, "Error 1033", ClassTunnelDown},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: tc.status, Header: http.Header{}}
			for k, v := range tc.headers {
				resp.Header.Set(k, v)
			}
			if got := Classify(resp, []byte(tc.body)); got != tc.want {
				t.Errorf("Classify() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClassifyError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"no error", nil, ClassOK},
		{"dns failure", &net.DNSError{Err: "no such host", Name: "x.example.com"}, ClassDNSError},
		{"timeout", &url.Error{Op: "Get", Err: timeoutError{}}, ClassTimeout},
		{"tls failure", &url.Error{Op: "Get", Err: errString("tls: handshake failure")}, ClassTLSError},
		{"other", errString("connection reset"), ClassHTTPError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyError(tc.err); got != tc.want {
				t.Errorf("ClassifyError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestClassHelpers(t *testing.T) {
	if !IsHealthy(ClassOK) || IsHealthy(ClassAccessChallenge) {
		t.Error("only ClassOK should count as healthy")
	}
	for _, class := range []string{ClassTunnelDown, ClassOriginError, ClassTimeout, ClassDNSError, ClassTLSError, ClassHTTPError} {
		if !IsFailure(class) {
			t.Errorf("IsFailure(%q) = false, want true", class)
		}
	}
	for _, class := range []string{ClassOK, ClassAccessChallenge, ClassAccessDenied, ClassEdgeCached} {
		if IsFailure(class) {
			t.Errorf("IsFailure(%q) = true, want false", class)
		}
	}
	if IsHealthy(ClassEdgeCached) {
		t.Error("got cached response healthy, want unverified")
	}
}

func newTestProber(t *testing.T, cfg Config, h http.HandlerFunc) (*Prober, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// Route every hostname at the test server while keeping the real request path.
	transport := &http.Transport{
		Proxy: func(r *http.Request) (*url.URL, error) { return url.Parse(srv.URL) },
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   cfg.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return New(cfg, slog.New(slog.DiscardHandler), WithHTTPClient(client), WithScheme("http")), srv
}

func TestProbeSendsServiceTokenHeaders(t *testing.T) {
	var gotID, gotSecret, gotPath, gotHost string
	p, _ := newTestProber(t, Config{
		Timeout:      2 * time.Second,
		Concurrency:  1,
		ClientID:     "client-id.access",
		ClientSecret: "super-secret",
	}, func(w http.ResponseWriter, r *http.Request) {
		gotID = r.Header.Get("CF-Access-Client-Id")
		gotSecret = r.Header.Get("CF-Access-Client-Secret")
		gotPath = r.URL.Path
		gotHost = r.Host
		w.WriteHeader(http.StatusOK)
	})

	result := p.Probe(t.Context(), Target{Hostname: "app.example.com", UseServiceToken: true})
	if result.Class != ClassOK {
		t.Fatalf("Class = %q, want %q", result.Class, ClassOK)
	}
	if gotID != "client-id.access" || gotSecret != "super-secret" {
		t.Errorf("headers = %q/%q, want the configured service token", gotID, gotSecret)
	}
	if gotPath != "/" {
		t.Errorf("path = %q, want \"/\" only", gotPath)
	}
	if gotHost != "app.example.com" {
		t.Errorf("host = %q, want the probed hostname", gotHost)
	}
}

// A public hostname has no Access application, so cloudflared would hand the
// credential straight to the origin.
func TestProbeOmitsTokenForPublicHostname(t *testing.T) {
	var gotID, gotSecret string
	p, _ := newTestProber(t, Config{
		Timeout:      2 * time.Second,
		Concurrency:  1,
		ClientID:     "client-id.access",
		ClientSecret: "super-secret",
	}, func(w http.ResponseWriter, r *http.Request) {
		gotID = r.Header.Get("CF-Access-Client-Id")
		gotSecret = r.Header.Get("CF-Access-Client-Secret")
		w.WriteHeader(http.StatusOK)
	})

	result := p.Probe(t.Context(), Target{Hostname: "public-app.example.com", UseServiceToken: false})
	if result.Class != ClassOK {
		t.Errorf("Class = %q, want %q — a public hostname is probed end to end without a token", result.Class, ClassOK)
	}
	if gotID != "" || gotSecret != "" {
		t.Errorf("headers = %q/%q, want none for a public hostname", gotID, gotSecret)
	}
}

func TestProbeWithoutTokenOmitsHeaders(t *testing.T) {
	var seen bool
	p, _ := newTestProber(t, Config{Timeout: 2 * time.Second, Concurrency: 1},
		func(w http.ResponseWriter, r *http.Request) {
			seen = r.Header.Get("CF-Access-Client-Id") != ""
			w.Header().Set("Location", "https://team.cloudflareaccess.com/cdn-cgi/access/login/x")
			w.WriteHeader(http.StatusFound)
		})

	if p.HasServiceToken() {
		t.Error("HasServiceToken() = true, want false")
	}
	result := p.Probe(t.Context(), Target{Hostname: "app.example.com", UseServiceToken: true})
	if seen {
		t.Error("service token headers were sent without a configured token")
	}
	if result.Class != ClassAccessChallenge {
		t.Errorf("Class = %q, want %q", result.Class, ClassAccessChallenge)
	}
}

func TestProbeDoesNotFollowRedirects(t *testing.T) {
	var requests int
	p, _ := newTestProber(t, Config{Timeout: 2 * time.Second, Concurrency: 1},
		func(w http.ResponseWriter, r *http.Request) {
			requests++
			w.Header().Set("Location", "https://elsewhere.example.com/")
			w.WriteHeader(http.StatusFound)
		})

	p.Probe(t.Context(), Target{Hostname: "app.example.com", UseServiceToken: true})
	if requests != 1 {
		t.Errorf("requests = %d, want the redirect not to be followed", requests)
	}
}

func TestProbeRejectsInvalidHostname(t *testing.T) {
	p, _ := newTestProber(t, Config{Timeout: time.Second, Concurrency: 1},
		func(w http.ResponseWriter, r *http.Request) {
			t.Error("no request should be sent for an invalid hostname")
		})

	for _, hostname := range []string{"", "*.example.com", "http://evil.example.com", "host/../path", "no-dot"} {
		result := p.Probe(t.Context(), Target{Hostname: hostname})
		if result.Class != ClassHTTPError || result.Error != "invalid hostname" {
			t.Errorf("Probe(%q) = %+v, want it rejected", hostname, result)
		}
	}
}

func TestProbeRedactsServiceTokenFromErrors(t *testing.T) {
	const secret = "super-secret"
	p := New(Config{Timeout: 50 * time.Millisecond, Concurrency: 1, ClientID: "id", ClientSecret: secret},
		slog.New(slog.DiscardHandler),
		WithHTTPClient(&http.Client{
			Timeout:   50 * time.Millisecond,
			Transport: errorTransport{msg: "dial failed for " + secret},
		}))

	result := p.Probe(t.Context(), Target{Hostname: "app.example.com", UseServiceToken: true})
	if strings.Contains(result.Error, secret) {
		t.Errorf("Error = %q, want the service token redacted", result.Error)
	}
	if !strings.Contains(result.Error, "[redacted]") {
		t.Errorf("Error = %q, want a redaction marker", result.Error)
	}
}

func TestProbeAllRespectsConcurrency(t *testing.T) {
	var (
		mu      sync.Mutex
		current int
		peak    int
	)
	p, _ := newTestProber(t, Config{Timeout: 2 * time.Second, Concurrency: 2},
		func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			current++
			peak = max(peak, current)
			mu.Unlock()

			time.Sleep(20 * time.Millisecond)

			mu.Lock()
			current--
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		})

	hostnames := []string{"a.example.com", "b.example.com", "c.example.com", "d.example.com", "e.example.com"}
	targets := make([]Target, 0, len(hostnames))
	for _, hostname := range hostnames {
		targets = append(targets, Target{Hostname: hostname, UseServiceToken: true})
	}
	results := p.ProbeAll(t.Context(), targets)

	if len(results) != len(hostnames) {
		t.Fatalf("len(results) = %d, want %d", len(results), len(hostnames))
	}
	for i, r := range results {
		if r.Hostname != hostnames[i] {
			t.Errorf("results[%d].Hostname = %q, want %q (order must be stable)", i, r.Hostname, hostnames[i])
		}
		if r.Class != ClassOK {
			t.Errorf("results[%d].Class = %q, want %q", i, r.Class, ClassOK)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if peak > 2 {
		t.Errorf("peak concurrency = %d, want at most 2", peak)
	}
}
