package cloudflare

import (
	"context"
	"net/url"
	"strings"
)

// ListTunnels returns every non-deleted Cloudflare Tunnel in the account. The
// embedded connections already carry status, colo and origin IP, so no extra
// request is needed for the dashboard overview.
func (c *Client) ListTunnels(ctx context.Context) ([]Tunnel, error) {
	q := url.Values{}
	q.Set("is_deleted", "false")
	return listAll[Tunnel](ctx, c, c.accountPath("cfd_tunnel"), q)
}

// GetTunnel fetches a single tunnel.
func (c *Client) GetTunnel(ctx context.Context, tunnelID string) (Tunnel, error) {
	t, _, err := getJSON[Tunnel](ctx, c, c.accountPath("cfd_tunnel", tunnelID), nil)
	return t, err
}

// ListConnectors returns the running cloudflared instances of a tunnel,
// including the fields the tunnel list omits: architecture, feature flags,
// process start time and the configuration version each connector has applied.
func (c *Client) ListConnectors(ctx context.Context, tunnelID string) ([]Connector, error) {
	connectors, _, err := getJSON[[]Connector](ctx, c, c.accountPath("cfd_tunnel", tunnelID, "connections"), nil)
	return connectors, err
}

// GetConfiguration returns the remotely managed ingress configuration.
func (c *Client) GetConfiguration(ctx context.Context, tunnelID string) (TunnelConfiguration, error) {
	cfg, _, err := getJSON[TunnelConfiguration](ctx, c, c.accountPath("cfd_tunnel", tunnelID, "configurations"), nil)
	return cfg, err
}

// IsHTTPService reports whether an ingress rule points at an HTTP origin and is
// therefore a valid probe target. SSH, RDP, raw TCP and the http_status
// catch-all cannot be checked with an HTTP request.
func (r IngressRule) IsHTTPService() bool {
	return strings.HasPrefix(r.Service, "http://") || strings.HasPrefix(r.Service, "https://")
}

// IsCatchAll reports whether the rule is the trailing fallback rule, which has
// no hostname.
func (r IngressRule) IsCatchAll() bool {
	return r.Hostname == ""
}
