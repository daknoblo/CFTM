// Package release resolves the latest published cloudflared version.
package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/daknoblo/CFTM/internal/version"
)

// latestReleaseURL is the unauthenticated GitHub endpoint. At 60 requests per
// hour per IP the default twelve-hour interval is far below the limit.
const latestReleaseURL = "https://api.github.com/repos/cloudflare/cloudflared/releases/latest"

// maxResponseBytes caps the body read from GitHub.
const maxResponseBytes = 1 << 20

// Checker fetches the newest cloudflared release tag.
type Checker struct {
	httpClient *http.Client
	url        string
}

// Option customizes a Checker.
type Option func(*Checker)

// WithHTTPClient supplies a custom HTTP client.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Checker) { c.httpClient = h }
}

// WithURL overrides the release endpoint, used by tests.
func WithURL(u string) Option {
	return func(c *Checker) { c.url = u }
}

// New returns a Checker.
func New(opts ...Option) *Checker {
	c := &Checker{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		url:        latestReleaseURL,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Latest returns the newest cloudflared version, for example "2026.4.0".
func (c *Checker) Latest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil) //nolint:gosec // G704: the endpoint is a package constant
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "cftm/"+version.Version)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("release: fetch latest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release: fetch latest: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("release: read body: %w", err)
	}

	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("release: decode response: %w", err)
	}

	tag := strings.TrimPrefix(strings.TrimSpace(payload.TagName), "v")
	if tag == "" {
		return "", fmt.Errorf("release: response contained no tag_name")
	}
	return tag, nil
}
