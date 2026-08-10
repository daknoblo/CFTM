package cloudflare

import "testing"

func TestPolicyCoversEveryTunnelWhenUnfiltered(t *testing.T) {
	p := NotificationPolicy{AlertType: AlertTunnelHealth, Enabled: true}

	if !p.Covers("any-id", "any-name") {
		t.Error("an unfiltered policy must cover every tunnel in the account")
	}
}

func TestPolicyMatchesByIDOrName(t *testing.T) {
	byID := NotificationPolicy{
		Enabled: true,
		Filters: NotificationFilter{TunnelID: []string{"ABC-123"}},
	}
	byName := NotificationPolicy{
		Enabled: true,
		Filters: NotificationFilter{TunnelName: []string{"edge"}},
	}

	// Cloudflare is inconsistent about identifier case, so matching is not.
	if !byID.Covers("abc-123", "edge") {
		t.Error("tunnel ID matching should ignore case")
	}
	if !byName.Covers("other", "EDGE") {
		t.Error("tunnel name matching should ignore case")
	}
	if byID.Covers("different", "edge") {
		t.Error("a policy filtered to another tunnel must not match")
	}
}

func TestDisabledPolicyCoversNothing(t *testing.T) {
	p := NotificationPolicy{AlertType: AlertTunnelHealth, Enabled: false}

	if p.Covers("any", "any") {
		t.Error("a disabled policy must not count as coverage")
	}
}

func TestDeliversToCountsEveryDestination(t *testing.T) {
	silent := NotificationPolicy{}
	if got := silent.DeliversTo(); got != 0 {
		t.Errorf("DeliversTo() = %d for a policy with no destination, want 0", got)
	}

	wired := NotificationPolicy{Mechanisms: AlertMechanisms{
		Email:    []map[string]any{{"id": "a@example.com"}},
		Webhooks: []map[string]any{{"id": "hook"}},
	}}
	if got, want := wired.DeliversTo(), 2; got != want {
		t.Errorf("DeliversTo() = %d, want %d", got, want)
	}
}
