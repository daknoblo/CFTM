package cloudflare

import (
	"context"
	"strings"
)

// Alert types that concern a tunnel. Cloudflare defines many more; these are the
// two that fire when a tunnel's health or configuration changes.
const (
	AlertTunnelHealth = "tunnel_health_event"
	AlertTunnelUpdate = "tunnel_update_event"
)

// NotificationPolicy is one alerting rule configured in the account.
type NotificationPolicy struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	AlertType   string             `json:"alert_type"`
	Enabled     bool               `json:"enabled"`
	Filters     NotificationFilter `json:"filters"`
	Mechanisms  AlertMechanisms    `json:"mechanisms"`
}

// NotificationFilter narrows an alert to a subset of events. Only the fields
// that matter for tunnels are decoded.
type NotificationFilter struct {
	TunnelID   []string `json:"tunnel_id"`
	TunnelName []string `json:"tunnel_name"`
	NewStatus  []string `json:"new_status"`
}

// AlertMechanisms lists where a policy delivers to. Only the count is used, so
// the identifiers stay untyped.
type AlertMechanisms struct {
	Email     []map[string]any `json:"email"`
	PagerDuty []map[string]any `json:"pagerduty"`
	Webhooks  []map[string]any `json:"webhooks"`
}

// ListNotificationPolicies returns the account's alerting rules.
func (c *Client) ListNotificationPolicies(ctx context.Context) ([]NotificationPolicy, error) {
	policies, _, err := getJSON[[]NotificationPolicy](ctx, c, c.accountPath("alerting", "v3", "policies"), nil)
	return policies, err
}

// DeliversTo reports how many destinations a policy notifies. A policy with
// none is configured but silent.
func (p NotificationPolicy) DeliversTo() int {
	return len(p.Mechanisms.Email) + len(p.Mechanisms.PagerDuty) + len(p.Mechanisms.Webhooks)
}

// Covers reports whether the policy would fire for the given tunnel. An empty
// filter means the policy applies to every tunnel in the account.
func (p NotificationPolicy) Covers(tunnelID, tunnelName string) bool {
	if !p.Enabled {
		return false
	}
	if len(p.Filters.TunnelID) == 0 && len(p.Filters.TunnelName) == 0 {
		return true
	}
	for _, id := range p.Filters.TunnelID {
		if strings.EqualFold(id, tunnelID) {
			return true
		}
	}
	for _, name := range p.Filters.TunnelName {
		if strings.EqualFold(name, tunnelName) {
			return true
		}
	}
	return false
}
