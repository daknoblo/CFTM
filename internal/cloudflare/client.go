// Package cloudflare is a read-only client for the Cloudflare Tunnel and
// Access APIs.
package cloudflare

import (
	"bytes"
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

	// graphQLBudgetReserve is the same idea for the Analytics API, which has
	// its own and much smaller quota of 300 queries per five minutes.
	graphQLBudgetReserve = 25
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

	mu          sync.RWMutex
	rate        RateLimit
	graphQLRate RateLimit
}

// request is one outgoing call.
type request struct {
	method string
	// path names the endpoint in errors and logs; url may also carry a query.
	path string
	url  string
	body []byte
	// graphQL calls draw on a separate quota, so their headers must not move
	// the REST budget guard.
	graphQL bool
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

// GraphQLRateLimit returns the quota state of the Analytics API, which is
// counted separately from the REST one.
func (c *Client) GraphQLRateLimit() RateLimit {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.graphQLRate
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
	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	return c.send(ctx, request{method: http.MethodGet, path: path, url: endpoint})
}

// send runs one request through the budget guard and the retry loop.
func (c *Client) send(ctx context.Context, req request) ([]byte, error) {
	if err := c.checkBudget(req.graphQL); err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			if err := sleepCtx(ctx, c.retryDelay(attempt)); err != nil {
				return nil, err
			}
		}
		body, err := c.attempt(ctx, req)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retryable(err) {
			return nil, err
		}
		c.log.Debug("cloudflare request failed, retrying",
			"path", req.path, "attempt", attempt, "err", err)
	}
	return nil, lastErr
}

func (c *Client) attempt(ctx context.Context, req request) ([]byte, error) {
	var body io.Reader
	if req.body != nil {
		body = bytes.NewReader(req.body)
	}
	// The URL is assembled from the fixed API base URL plus internal path
	// constants and escaped identifiers; it is never caller-controlled.
	httpReq, err := http.NewRequestWithContext(ctx, req.method, req.url, body) //nolint:gosec // G704: url is not user-controlled
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.token)
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "cftm/"+version.Version)
	if req.body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	c.recordRateLimit(resp.Header, resp.StatusCode, req.graphQL)

	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("cloudflare: %s %s: read body: %w", req.method, req.path, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, newAPIError(resp, req.method, req.path, payload)
	}
	return payload, nil
}

func newAPIError(resp *http.Response, method, path string, body []byte) *APIError {
	apiErr := &APIError{
		StatusCode: resp.StatusCode,
		Method:     method,
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
func (c *Client) checkBudget(graphQL bool) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	rate, reserve := c.rate, c.budgetReserve
	if graphQL {
		rate, reserve = c.graphQLRate, graphQLBudgetReserve
	}
	if !rate.Known() || time.Now().After(rate.ResetAt) {
		return nil
	}
	if rate.Remaining > reserve {
		return nil
	}
	return fmt.Errorf("%w: %d requests left, resets at %s",
		ErrBudgetGuard, rate.Remaining, rate.ResetAt.Format(time.RFC3339))
}

// recordRateLimit stores the quota state advertised by the response headers.
func (c *Client) recordRateLimit(h http.Header, statusCode int, graphQL bool) {
	now := time.Now()
	remaining, resetIn, haveLimit := parseRateLimitHeader(h.Get("Ratelimit"))
	quota, havePolicy := parseRateLimitPolicy(h.Get("Ratelimit-Policy"))

	c.mu.Lock()
	defer c.mu.Unlock()

	rate := &c.rate
	if graphQL {
		rate = &c.graphQLRate
	}

	if haveLimit {
		rate.Remaining = remaining
		rate.ResetAt = now.Add(resetIn)
		rate.ObservedAt = now
	}
	if havePolicy {
		rate.Quota = quota
	}
	if statusCode == http.StatusTooManyRequests {
		retryAfter := parseRetryAfter(h.Get("Retry-After"))
		if retryAfter <= 0 {
			retryAfter = 5 * time.Minute
		}
		rate.Remaining = 0
		rate.ResetAt = now.Add(retryAfter)
		rate.ObservedAt = now
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
