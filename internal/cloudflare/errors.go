package cloudflare

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrBudgetGuard is returned before a request is sent when the remaining
// Cloudflare API quota has dropped below the configured reserve. The quota is
// shared with the dashboard and every other token of the same user, so the
// monitor yields rather than starving the Terraform pipeline.
var ErrBudgetGuard = errors.New("cloudflare: api budget reserve reached")

// ErrorDetail is a single error entry from the Cloudflare response envelope.
type ErrorDetail struct {
	Code             int    `json:"code"`
	Message          string `json:"message"`
	DocumentationURL string `json:"documentation_url"`
}

// APIError reports a non-2xx response or an envelope with success=false.
type APIError struct {
	StatusCode int
	Method     string
	Path       string
	Details    []ErrorDetail
	RetryAfter time.Duration
}

// Error implements error.
func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "cloudflare: %s %s: status %d", e.Method, e.Path, e.StatusCode)
	for i, d := range e.Details {
		if i == 0 {
			b.WriteString(": ")
		} else {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "%d %s", d.Code, d.Message)
	}
	return b.String()
}

// IsAuth reports whether the token was rejected, which no retry can fix.
func (e *APIError) IsAuth() bool {
	return e.StatusCode == 401 || e.StatusCode == 403
}

// IsRateLimited reports whether the shared 1200-per-5-minutes quota was hit.
func (e *APIError) IsRateLimited() bool {
	return e.StatusCode == 429
}

// IsNotFound reports whether the resource no longer exists.
func (e *APIError) IsNotFound() bool {
	return e.StatusCode == 404
}
