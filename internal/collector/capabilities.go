package collector

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/daknoblo/CFTM/internal/cloudflare"
)

// MetaCapabilities caches what the API token was actually allowed to read.
const MetaCapabilities = "api_capabilities"

// Capability keys, one per area of the API that needs its own token permission.
const (
	CapTunnels       = "tunnels"
	CapAccessApps    = "access_apps"
	CapServiceTokens = "service_tokens"
	CapNotifications = "notifications"
	CapAccessLogins  = "access_logins"
)

// Capability states.
const (
	CapOK        = "ok"
	CapForbidden = "forbidden"
	CapError     = "error"
	CapDisabled  = "disabled"
)

// Capability records the last outcome of calling one area of the API.
type Capability struct {
	State     string    `json:"state"`
	Detail    string    `json:"detail,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
}

// RequiredPermission names the Cloudflare token permission an area needs.
func RequiredPermission(key string) string {
	switch key {
	case CapTunnels:
		return "Account : Cloudflare Tunnel : Read"
	case CapAccessApps:
		return "Account : Access: Apps and Policies : Read"
	case CapServiceTokens:
		return "Account : Access: Service Tokens : Read"
	case CapNotifications:
		return "Account : Notifications : Read"
	case CapAccessLogins:
		return "Account : Access: Audit Logs : Read"
	default:
		return ""
	}
}

// capabilities is the in-memory mirror of the cached map, so recording an
// outcome does not need a read-modify-write per API call.
type capabilities struct {
	mu    sync.Mutex
	known map[string]Capability
}

// record stores the outcome of one call. A nil error means the token is allowed;
// a 401 or 403 means the permission is missing, which is not a failure worth
// logging every cycle but is worth showing on the About page.
func (c *Collector) record(ctx context.Context, key string, err error) {
	state, detail := CapOK, ""
	switch {
	case err == nil:
	case isForbidden(err):
		state, detail = CapForbidden, "the token does not carry this permission"
	default:
		state, detail = CapError, err.Error()
	}

	c.caps.mu.Lock()
	if c.caps.known == nil {
		c.caps.known = map[string]Capability{}
	}
	c.caps.known[key] = Capability{State: state, Detail: detail, CheckedAt: time.Now()}
	snapshot, marshalErr := json.Marshal(c.caps.known)
	c.caps.mu.Unlock()

	if marshalErr != nil {
		c.log.Warn("encoding capabilities failed", "err", marshalErr)
		return
	}
	if err := c.store.SetMeta(ctx, MetaCapabilities, string(snapshot), time.Now()); err != nil {
		c.log.Warn("caching capabilities failed", "err", err)
	}
}

func isForbidden(err error) bool {
	var apiErr *cloudflare.APIError
	return errors.As(err, &apiErr) && apiErr.IsAuth()
}

// Capabilities returns what the token was last observed to be allowed to read.
func (c *Collector) Capabilities(ctx context.Context) map[string]Capability {
	raw, _, err := c.store.GetMeta(ctx, MetaCapabilities)
	if err != nil || raw == "" {
		return nil
	}
	var known map[string]Capability
	if err := json.Unmarshal([]byte(raw), &known); err != nil {
		c.log.Warn("decoding cached capabilities failed", "err", err)
		return nil
	}
	return known
}
