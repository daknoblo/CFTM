package cloudflare

import (
	"context"
	"net/url"
	"strings"
)

// Zone is the part of a Cloudflare zone the monitor needs: the tag identifies
// it to the Analytics API, the name maps a hostname onto it.
type Zone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Plan struct {
		Name string `json:"name"`
	} `json:"plan"`
}

// ListZones returns the active zones of this account. Zones change about once
// a year, so the result is worth caching rather than fetching per cycle.
func (c *Client) ListZones(ctx context.Context) ([]Zone, error) {
	query := url.Values{}
	query.Set("account.id", c.accountID)
	query.Set("status", "active")
	return listAll[Zone](ctx, c, "/zones", query)
}

// ZoneFor returns the zone serving a hostname. The longest matching suffix
// wins, so a delegated subdomain zone beats its parent, and a bare suffix
// match is rejected: "notexample.com" does not belong to "example.com".
func ZoneFor(zones []Zone, hostname string) (Zone, bool) {
	host := strings.ToLower(strings.TrimSpace(hostname))

	var best Zone
	var found bool
	for _, z := range zones {
		name := strings.ToLower(z.Name)
		if host != name && !strings.HasSuffix(host, "."+name) {
			continue
		}
		if !found || len(name) > len(best.Name) {
			best, found = z, true
		}
	}
	return best, found
}
