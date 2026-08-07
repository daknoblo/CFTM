// Package prober checks tunnel hostnames end to end over the Cloudflare edge.
package prober

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/daknoblo/CFTM/internal/store"
	"github.com/daknoblo/CFTM/internal/version"
)

// maxBodyBytes caps how much of a response is read; only Cloudflare's error
// markers are of interest and they appear early in the page.
const maxBodyBytes = 8 << 10

// hostnamePattern accepts ordinary DNS names only. Wildcard ingress entries are
// rejected because there is nothing concrete to request.
var hostnamePattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)+$`)

// Config controls probing behavior.
type Config struct {
	Timeout      time.Duration
	Concurrency  int
	ClientID     string
	ClientSecret string
}

// Prober performs HTTP checks against public tunnel hostnames.
type Prober struct {
	httpClient *http.Client
	cfg        Config
	log        *slog.Logger
	scheme     string
}

// Option customizes a Prober.
type Option func(*Prober)

// WithHTTPClient supplies a custom HTTP client.
func WithHTTPClient(h *http.Client) Option {
	return func(p *Prober) { p.httpClient = h }
}

// WithScheme overrides the request scheme, used by tests.
func WithScheme(scheme string) Option {
	return func(p *Prober) { p.scheme = scheme }
}

// New returns a Prober. Redirects are never followed so an Access challenge is
// observable and the probe cannot be steered at an unrelated host.
func New(cfg Config, log *slog.Logger, opts ...Option) *Prober {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 4
	}

	p := &Prober{
		cfg:    cfg,
		log:    log,
		scheme: "https",
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// HasServiceToken reports whether probes can authenticate against Access.
func (p *Prober) HasServiceToken() bool {
	return p.cfg.ClientID != "" && p.cfg.ClientSecret != ""
}

// Target is one hostname to probe.
type Target struct {
	Hostname string

	// UseServiceToken is false for hostnames without an Access application.
	// Nothing at the edge would consume the credential there, so cloudflared
	// forwards it to the origin like any other header.
	UseServiceToken bool
}

// Targets extracts the probeable hostnames from an ingress inventory. SSH, RDP
// and the http_status catch-all cannot be checked with an HTTP request.
//
// publicHostnames names the hostnames known to have no Access application. It
// is only populated once an Access audit has succeeded; while it is empty every
// target carries the token, which is the safe default because a hostname that
// is in fact guarded would otherwise never be probed past the login page.
func Targets(rules []store.IngressRule, publicHostnames map[string]bool) []Target {
	seen := map[string]bool{}
	var out []Target
	for _, rule := range rules {
		if !isHTTPService(rule.Service) || !hostnamePattern.MatchString(rule.Hostname) {
			continue
		}
		if seen[rule.Hostname] {
			continue
		}
		seen[rule.Hostname] = true
		out = append(out, Target{
			Hostname:        rule.Hostname,
			UseServiceToken: !publicHostnames[strings.ToLower(rule.Hostname)],
		})
	}
	return out
}

// ProbeAll checks every target, bounded by the configured concurrency.
func (p *Prober) ProbeAll(ctx context.Context, targets []Target) []store.ProbeResult {
	results := make([]store.ProbeResult, len(targets))
	sem := make(chan struct{}, p.cfg.Concurrency)

	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i] = store.ProbeResult{
					Hostname:  target.Hostname,
					CheckedAt: time.Now(),
					Class:     ClassTimeout,
					Error:     ctx.Err().Error(),
				}
				return
			}

			// Stagger requests so a burst does not hit one tunnel at once.
			if err := stagger(ctx); err != nil {
				results[i] = store.ProbeResult{
					Hostname:  target.Hostname,
					CheckedAt: time.Now(),
					Class:     ClassTimeout,
					Error:     err.Error(),
				}
				return
			}
			results[i] = p.Probe(ctx, target)
		}()
	}
	wg.Wait()

	return results
}

// Probe checks a single target and classifies the outcome.
func (p *Prober) Probe(ctx context.Context, target Target) store.ProbeResult {
	started := time.Now()
	result := store.ProbeResult{Hostname: target.Hostname, CheckedAt: started}

	if !hostnamePattern.MatchString(target.Hostname) {
		result.Class = ClassHTTPError
		result.Error = "invalid hostname"
		return result
	}

	// The URL is built from a validated hostname taken from the tunnel's own
	// ingress configuration, and only ever requests the root path.
	endpoint := p.scheme + "://" + target.Hostname + "/"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil) //nolint:gosec // G704: hostname is validated and comes from the tunnel configuration
	if err != nil {
		result.Class = ClassHTTPError
		result.Error = err.Error()
		return result
	}
	req.Header.Set("User-Agent", "cftm/"+version.Version)
	req.Header.Set("Accept", "*/*")
	if target.UseServiceToken && p.HasServiceToken() {
		req.Header.Set("CF-Access-Client-Id", p.cfg.ClientID)
		req.Header.Set("CF-Access-Client-Secret", p.cfg.ClientSecret)
	}

	resp, err := p.httpClient.Do(req)
	result.LatencyMS = int(time.Since(started).Milliseconds())
	if err != nil {
		result.Class = ClassifyError(err)
		result.Error = redactSecrets(err.Error(), p.cfg.ClientSecret)
		return result
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		result.StatusCode = resp.StatusCode
		result.Class = ClassHTTPError
		result.Error = fmt.Sprintf("read body: %v", err)
		return result
	}

	result.StatusCode = resp.StatusCode
	result.Class = Classify(resp, body)
	return result
}

// redactSecrets makes sure a transport error can never leak the service token.
func redactSecrets(message, secret string) string {
	if secret == "" {
		return message
	}
	return strings.ReplaceAll(message, secret, "[redacted]")
}

func isHTTPService(service string) bool {
	return strings.HasPrefix(service, "http://") || strings.HasPrefix(service, "https://")
}

// stagger sleeps for a short random delay.
func stagger(ctx context.Context) error {
	delay := time.Duration(rand.Int64N(int64(250 * time.Millisecond))) //nolint:gosec // G404: request spacing needs no cryptographic randomness
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
