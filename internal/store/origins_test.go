package store

import (
	"testing"
	"time"
)

func TestRequestOriginsRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	origins := []RequestOrigin{
		{
			Source: OriginSourceHTTP, Hostname: "a.example.com", Path: "/api",
			Country: "DE", Allowed: 10, Requests: 10,
			Window: now.Add(-24 * time.Hour), LastAt: now,
		},
		{
			Source: OriginSourceHTTP, Hostname: "a.example.com", Path: "/api",
			Country: "US", Denied: 4, Requests: 4, Sampled: true,
			Window: now.Add(-24 * time.Hour), LastAt: now,
		},
	}
	if err := s.ReplaceRequestOrigins(ctx, origins); err != nil {
		t.Fatalf("ReplaceRequestOrigins() error = %v", err)
	}

	stored, err := s.RequestOrigins(ctx)
	if err != nil {
		t.Fatalf("RequestOrigins() error = %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("len(stored) = %d, want 2", len(stored))
	}
	// Busiest first.
	if stored[0].Country != "DE" || stored[0].Requests != 10 {
		t.Errorf("stored[0] = %+v, want the DE bucket of 10 first", stored[0])
	}
	if !stored[1].Sampled {
		t.Errorf("stored[1] = %+v, want the sampled flag preserved", stored[1])
	}
	if !stored[0].Window.Equal(now.Add(-24 * time.Hour)) {
		t.Errorf("window = %v, want %v", stored[0].Window, now.Add(-24*time.Hour))
	}

	// No client IP may reach the database.
	if err := s.ReplaceRequestOrigins(ctx, origins); err != nil {
		t.Fatalf("second ReplaceRequestOrigins() error = %v", err)
	}
	again, err := s.RequestOrigins(ctx)
	if err != nil {
		t.Fatalf("RequestOrigins() error = %v", err)
	}
	if len(again) != 2 || again[0].Requests != 10 {
		t.Errorf("after replace = %+v, want the table swapped rather than appended", again)
	}
}

// One hostname can be reported by several sources, and a country can serve
// several hostnames; the totals must fold both without double counting.
func TestOriginCountryTotals(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	err := s.ReplaceRequestOrigins(ctx, []RequestOrigin{
		{Source: OriginSourceHTTP, Hostname: "a.example.com", Country: "DE", Allowed: 5, Requests: 5, LastAt: now},
		{Source: OriginSourceHTTP, Hostname: "b.example.com", Country: "DE", Allowed: 3, Requests: 3, LastAt: now},
		{Source: OriginSourceLogin, Hostname: "", Country: "US", Denied: 2, Requests: 2, Sampled: true, LastAt: now},
	})
	if err != nil {
		t.Fatalf("ReplaceRequestOrigins() error = %v", err)
	}

	totals, err := s.OriginCountryTotals(ctx)
	if err != nil {
		t.Fatalf("OriginCountryTotals() error = %v", err)
	}
	if len(totals) != 2 {
		t.Fatalf("len(totals) = %d, want 2", len(totals))
	}
	if totals[0].Country != "DE" || totals[0].Requests != 8 || totals[0].Allowed != 8 {
		t.Errorf("totals[0] = %+v, want DE folded to 8", totals[0])
	}
	if totals[1].Country != "US" || totals[1].Denied != 2 || !totals[1].Sampled {
		t.Errorf("totals[1] = %+v, want the US bucket with the sampled flag", totals[1])
	}
}

func TestRequestOriginsEmpty(t *testing.T) {
	s := newTestStore(t)

	origins, err := s.RequestOrigins(t.Context())
	if err != nil {
		t.Fatalf("RequestOrigins() error = %v", err)
	}
	if len(origins) != 0 {
		t.Errorf("origins = %+v, want none before the first collection", origins)
	}
}
