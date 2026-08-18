package cloudflare

import (
	"context"
	"strings"
)

// ListAccessApps returns every Cloudflare Access application in the account.
func (c *Client) ListAccessApps(ctx context.Context) ([]AccessApp, error) {
	return listAll[AccessApp](ctx, c, c.accountPath("access", "apps"), nil)
}

// ListAccessPolicies returns the policies attached to an Access application.
func (c *Client) ListAccessPolicies(ctx context.Context, appID string) ([]AccessPolicy, error) {
	return listAll[AccessPolicy](ctx, c, c.accountPath("access", "apps", appID, "policies"), nil)
}

// ListServiceTokens returns the account's Access service tokens. Only metadata
// is returned; the client secret is never retrievable after creation.
func (c *Client) ListServiceTokens(ctx context.Context) ([]ServiceToken, error) {
	return listAll[ServiceToken](ctx, c, c.accountPath("access", "service_tokens"), nil)
}

// Domains returns every hostname the application protects.
func (a AccessApp) Domains() []string {
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		d = normalizeDomain(d)
		if d == "" || seen[d] {
			return
		}
		seen[d] = true
		out = append(out, d)
	}
	add(a.Domain)
	for _, d := range a.SelfHostedDomains {
		add(d)
	}
	return out
}

// RawDomains returns the configured domains with their path intact. A
// path-scoped application only covers part of a host, which the normalized
// form cannot express.
func (a AccessApp) RawDomains() []string {
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		d = trimScheme(d)
		if d == "" || seen[d] {
			return
		}
		seen[d] = true
		out = append(out, d)
	}
	add(a.Domain)
	for _, d := range a.SelfHostedDomains {
		add(d)
	}
	return out
}

// normalizeDomain strips the scheme and any path so an Access domain such as
// "app.example.com/path" can be matched against an ingress hostname.
func normalizeDomain(d string) string {
	host, _, _ := strings.Cut(trimScheme(d), "/")
	return host
}

// trimScheme lowercases and drops the scheme but keeps the path.
func trimScheme(d string) string {
	d = strings.TrimSpace(strings.ToLower(d))
	if _, rest, found := strings.Cut(d, "://"); found {
		return rest
	}
	return d
}

// HasServiceTokenRule reports whether the policy admits a service token, which
// is what lets the monitor probe an origin end to end.
func (p AccessPolicy) HasServiceTokenRule() bool {
	for _, rule := range p.Include {
		for key := range rule {
			switch key {
			case "service_token", "any_valid_service_token":
				return true
			}
		}
	}
	return false
}

// IsBypass reports whether the policy lets traffic through without authentication.
func (p AccessPolicy) IsBypass() bool {
	return strings.EqualFold(p.Decision, "bypass")
}
