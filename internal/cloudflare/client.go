// Package cloudflare is a read-only client for the Cloudflare Tunnel and
// Access APIs.
package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daknoblo/CFTM/internal/version"
)

const (
	defaultBaseURL = "https://api.cloudflare.com/client/v4"
	defaultTimeout = 30 * time.Second

	maxAttempts = 3
	maxPages    = 50
	perPage     = 50

	// maxResponseBytes caps how much of a response body is read, so a rogue
	// or truncated reply cannot exhaust memory.
	maxResponseBytes = 8 << 20

	// defaultBudgetReserve keeps requests in hand for the Terraform pipeline
	// and the dashboard, which draw from the same 1200-per-5-minutes quota.
	defaultBudgetReserve = 100
)

// ResultInfo is the pagination block of the Cloudflare response envelope.
type ResultInfo struct {
	Count      int `json:"count"`
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	TotalCount int `json:"total_count"`
}

// RateLimit is the most recently observed quota state.
type RateLimit struct {
	Remaining  int       `json:"remaining"`
	Quota      int       `json:"quota"`
	ResetAt    time.Time `json:"resetAt"`
	ObservedAt time.Time `json:"observedAt"`
}

// Known reports whether any quota information has been observed yet.
func (r RateLimit) Known() bool { return !r.ObservedAt.IsZero() }

type envelope[T any] struct {
	Success    bool          `json:"success"`
	Errors     []ErrorDetail `json:"errors"`
	Messages   []ErrorDetail `json:"messages"`
	Result     T             `json:"result"`
	ResultInfo *ResultInfo   `json:"result_info"`
}

// Client talks to the Cloudflare REST API for a single account.
type Client struct {
	baseURL       string
	accountID     string
	token         string
	httpClient    *http.Client
	log           *slog.Logger
	backoffBase   time.Duration
	budgetReserve int

	mu   sync.RWMutex
	rate RateLimit
}

// Option customizes a Client.
type Option func(*Client)

// WithBaseURL overrides the API root, used by tests.
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimSuffix(u, "/") }
}

// WithHTTPClient supplies a custom HTTP client.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.httpClient = h }
}

// WithLogger attaches a logger.
func WithLogger(l *slog.Logger) Option {
	return func(c *Client) { c.log = l }
}

// WithBackoffBase sets the base delay between retries.
func WithBackoffBase(d time.Duration) Option {
	return func(c *Client) { c.backoffBase = d }
}

// WithBudgetReserve sets how many requests of the shared quota are left untouched.
func WithBudgetReserve(n int) Option {
	return func(c *Client) { c.budgetReserve = n }
}

// New returns a Client for the given account.
func New(accountID, token string, opts ...Option) *Client {
	c := &Client{
		baseURL:       defaultBaseURL,
		accountID:     accountID,
		token:         token,
		httpClient:    &http.Client{Timeout: defaultTimeout},
		log:           slog.Default(),
		backoffBase:   500 * time.Millisecond,
		budgetReserve: defaultBudgetReserve,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// AccountID returns the account this client is scoped to.
func (c *Client) AccountID() string { return c.accountID }

// RateLimit returns a snapshot of the observed quota state.
func (c *Client) RateLimit() RateLimit {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rate
}

func (c *Client) accountPath(parts ...string) string {
	segments := make([]string, 0, len(parts)+2)
	segments = append(segments, "accounts", url.PathEscape(c.accountID))
	for _, p := range parts {
		segments = append(segments, url.PathEscape(p))
	}
	return "/" + strings.Join(segments, "/")
}

// do issues a GET and returns the raw body, retrying transient failures.
func (c *Client) do(ctx context.Context, path string, query url.Values) ([]byte, error) {
	if err := c.checkBudget(); err != nil {
		return nil, err
	}

	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			if err := sleepCtx(ctx, c.retryDelay(attempt)); err != nil {
				return nil, err
			}
		}
		body, err := c.attempt(ctx, endpoint, path)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retryable(err) {
			return nil, err
		}
		c.log.Debug("cloudflare request failed, retrying",
			"path", path, "attempt", attempt, "err", err)
	}
	return nil, lastErr
}

func (c *Client) attempt(ctx context.Context, endpoint, path string) ([]byte, error) {
	// The endpoint is assembled from the fixed API base URL plus internal path
	// constants and escaped identifiers; it is never caller-controlled.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil) //nolint:gosec // G704: endpoint is not user-controlled
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "cftm/"+version.Version)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	c.recordRateLimit(resp.Header, resp.StatusCode)

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("cloudflare: GET %s: read body: %w", path, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, newAPIError(resp, path, body)
	}
	return body, nil
}

func newAPIError(resp *http.Response, path string, body []byte) *APIError {
	apiErr := &APIError{
		StatusCode: resp.StatusCode,
		Method:     http.MethodGet,
		Path:       path,
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
	}
	var env envelope[json.RawMessage]
	if err := json.Unmarshal(body, &env); err == nil {
		apiErr.Details = env.Errors
	}
	return apiErr
}

// retryable reports whether another attempt has a realistic chance of success.
// A 429 is deliberately excluded: the quota is blocked for a full five minutes,
// so retrying would only burn more of the shared budget.
func retryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode >= 500
	}
	return true
}

func (c *Client) retryDelay(attempt int) time.Duration {
	backoff := c.backoffBase * time.Duration(1<<(attempt-2))
	jitter := time.Duration(rand.Int64N(int64(c.backoffBase) + 1)) //nolint:gosec // G404: retry jitter needs no cryptographic randomness
	return backoff + jitter
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// checkBudget short-circuits before the shared quota is exhausted.
func (c *Client) checkBudget() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.rate.Known() || time.Now().After(c.rate.ResetAt) {
		return nil
	}
	if c.rate.Remaining > c.budgetReserve {
		return nil
	}
	return fmt.Errorf("%w: %d requests left, resets at %s",
		ErrBudgetGuard, c.rate.Remaining, c.rate.ResetAt.Format(time.RFC3339))
}

// recordRateLimit stores the quota state advertised by the response headers.
func (c *Client) recordRateLimit(h http.Header, statusCode int) {
	now := time.Now()
	remaining, resetIn, haveLimit := parseRateLimitHeader(h.Get("Ratelimit"))
	quota, havePolicy := parseRateLimitPolicy(h.Get("Ratelimit-Policy"))

	c.mu.Lock()
	defer c.mu.Unlock()

	if haveLimit {
		c.rate.Remaining = remaining
		c.rate.ResetAt = now.Add(resetIn)
		c.rate.ObservedAt = now
	}
	if havePolicy {
		c.rate.Quota = quota
	}
	if statusCode == http.StatusTooManyRequests {
		retryAfter := parseRetryAfter(h.Get("Retry-After"))
		if retryAfter <= 0 {
			retryAfter = 5 * time.Minute
		}
		c.rate.Remaining = 0
		c.rate.ResetAt = now.Add(retryAfter)
		c.rate.ObservedAt = now
	}
}

// parseRateLimitHeader reads `"default";r=50;t=30`, returning the tightest
// remaining quota across all listed policies.
func parseRateLimitHeader(v string) (remaining int, resetIn time.Duration, ok bool) {
	remaining = -1
	for _, item := range strings.Split(v, ",") {
		var (
			itemRemaining = -1
			itemReset     = -1
		)
		for _, part := range strings.Split(item, ";") {
			key, val, found := strings.Cut(strings.TrimSpace(part), "=")
			if !found {
				continue
			}
			n, err := strconv.Atoi(strings.TrimSpace(val))
			if err != nil {
				continue
			}
			switch strings.TrimSpace(key) {
			case "r":
				itemRemaining = n
			case "t":
				itemReset = n
			}
		}
		if itemRemaining < 0 {
			continue
		}
		if remaining < 0 || itemRemaining < remaining {
			remaining = itemRemaining
			if itemReset >= 0 {
				resetIn = time.Duration(itemReset) * time.Second
			}
		}
	}
	return remaining, resetIn, remaining >= 0
}

// parseRateLimitPolicy reads `"burst";q=100;w=60`.
func parseRateLimitPolicy(v string) (quota int, ok bool) {
	for _, item := range strings.Split(v, ",") {
		for _, part := range strings.Split(item, ";") {
			key, val, found := strings.Cut(strings.TrimSpace(part), "=")
			if !found || strings.TrimSpace(key) != "q" {
				continue
			}
			if n, err := strconv.Atoi(strings.TrimSpace(val)); err == nil && n > quota {
				quota, ok = n, true
			}
		}
	}
	return quota, ok
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// getJSON fetches a single envelope and unwraps its result.
func getJSON[T any](ctx context.Context, c *Client, path string, query url.Values) (T, *ResultInfo, error) {
	var zero T
	body, err := c.do(ctx, path, query)
	if err != nil {
		return zero, nil, err
	}
	var env envelope[T]
	if err := json.Unmarshal(body, &env); err != nil {
		return zero, nil, fmt.Errorf("cloudflare: GET %s: decode response: %w", path, err)
	}
	if !env.Success {
		return zero, nil, &APIError{
			StatusCode: http.StatusOK,
			Method:     http.MethodGet,
			Path:       path,
			Details:    env.Errors,
		}
	}
	return env.Result, env.ResultInfo, nil
}

// listAll walks every page of a paginated collection endpoint.
func listAll[T any](ctx context.Context, c *Client, path string, query url.Values) ([]T, error) {
	q := url.Values{}
	for k, v := range query {
		q[k] = v
	}
	q.Set("per_page", strconv.Itoa(perPage))

	var out []T
	for page := 1; page <= maxPages; page++ {
		q.Set("page", strconv.Itoa(page))
		items, info, err := getJSON[[]T](ctx, c, path, q)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if len(items) < perPage {
			break
		}
		if info != nil && info.TotalCount > 0 && len(out) >= info.TotalCount {
			break
		}
	}
	return out, nil
}
