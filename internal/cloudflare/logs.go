package cloudflare

import (
	"context"
	"net/url"
	"strconv"
	"time"
)

// maxAccessLogEntries caps one lookup. The log can hold tens of thousands of
// entries per day, and the point is a summary, not a copy of the audit trail.
const maxAccessLogEntries = 1000

// AccessRequest is one authentication event from the Access audit log.
type AccessRequest struct {
	Action     string    `json:"action"`
	Allowed    bool      `json:"allowed"`
	AppDomain  string    `json:"app_domain"`
	AppUID     string    `json:"app_uid"`
	Connection string    `json:"connection"`
	CreatedAt  Timestamp `json:"created_at"`
	IPAddress  string    `json:"ip_address"`
	UserEmail  string    `json:"user_email"`
}

// ListAccessRequests returns authentication events recorded since the given
// time, newest first.
func (c *Client) ListAccessRequests(ctx context.Context, since time.Time) ([]AccessRequest, error) {
	q := url.Values{}
	q.Set("since", since.UTC().Format(time.RFC3339))
	q.Set("limit", strconv.Itoa(maxAccessLogEntries))
	q.Set("direction", "desc")

	requests, _, err := getJSON[[]AccessRequest](ctx, c, c.accountPath("access", "logs", "access_requests"), q)
	return requests, err
}
