package collector

import (
	"testing"
	"time"

	"github.com/daknoblo/CFTM/internal/cloudflare"
)

func at(t time.Time) cloudflare.Timestamp { return cloudflare.Timestamp{Time: t} }

func TestSummarizeLoginsCountsDistinctUsersNotEvents(t *testing.T) {
	now := time.Now()
	requests := []cloudflare.AccessRequest{
		{AppDomain: "git.example.com", Allowed: true, UserEmail: "ada@example.com", CreatedAt: at(now)},
		{AppDomain: "git.example.com", Allowed: true, UserEmail: "ada@example.com", CreatedAt: at(now.Add(-time.Hour))},
		// Same person, different capitalization: still one user.
		{AppDomain: "git.example.com", Allowed: true, UserEmail: "Ada@Example.com", CreatedAt: at(now.Add(-2 * time.Hour))},
		{AppDomain: "git.example.com", Allowed: false, UserEmail: "eve@example.org", CreatedAt: at(now.Add(-3 * time.Hour))},
	}

	out := summarizeLogins(requests)
	if len(out) != 1 {
		t.Fatalf("summarizeLogins() produced %d rows, want 1", len(out))
	}
	got := out[0]
	if got.Allowed != 3 || got.Denied != 1 {
		t.Errorf("allowed=%d denied=%d, want 3 and 1", got.Allowed, got.Denied)
	}
	if got.Users != 2 {
		t.Errorf("Users = %d, want 2 distinct users", got.Users)
	}
	if !got.LastAt.Equal(now) {
		t.Errorf("LastAt = %s, want the newest event at %s", got.LastAt, now)
	}
}

func TestSummarizeLoginsFoldsThePathIntoTheDomain(t *testing.T) {
	requests := []cloudflare.AccessRequest{
		{AppDomain: "app.example.com/admin", Allowed: true},
		{AppDomain: "APP.example.com", Allowed: true},
	}

	out := summarizeLogins(requests)
	if len(out) != 1 {
		t.Fatalf("summarizeLogins() produced %d rows, want the path and case folded into one", len(out))
	}
	if got, want := out[0].AppDomain, "app.example.com"; got != want {
		t.Errorf("AppDomain = %q, want %q", got, want)
	}
}

func TestSummarizeLoginsSkipsEntriesWithoutADomain(t *testing.T) {
	out := summarizeLogins([]cloudflare.AccessRequest{{Allowed: true, UserEmail: "a@example.com"}})
	if len(out) != 0 {
		t.Errorf("summarizeLogins() produced %d rows for a domainless event, want 0", len(out))
	}
}
