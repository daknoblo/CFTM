package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestIgnoreFindingRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "cftm.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := t.Context()
	ref := FindingRef{Code: "access_unprotected", TunnelID: "t1", Hostname: "Public.Example.com"}

	if err := s.IgnoreFinding(ctx, ref, "Hostname is publicly reachable", time.Now()); err != nil {
		t.Fatalf("IgnoreFinding() error = %v", err)
	}
	// Muting twice must stay idempotent, the UI can post the same form again.
	if err := s.IgnoreFinding(ctx, ref, "Hostname is publicly reachable", time.Now()); err != nil {
		t.Fatalf("IgnoreFinding() second call error = %v", err)
	}

	ignored, err := s.IgnoredFindings(ctx)
	if err != nil {
		t.Fatalf("IgnoredFindings() error = %v", err)
	}
	if len(ignored) != 1 {
		t.Fatalf("len(IgnoredFindings()) = %d, want 1", len(ignored))
	}
	if got, want := ignored[0].Hostname, "public.example.com"; got != want {
		t.Errorf("Hostname = %q, want %q", got, want)
	}

	set, err := s.IgnoredFindingSet(ctx)
	if err != nil {
		t.Fatalf("IgnoredFindingSet() error = %v", err)
	}
	lookup := FindingRef{Code: ref.Code, TunnelID: ref.TunnelID, Hostname: "public.example.com"}
	if !set[lookup] {
		t.Errorf("IgnoredFindingSet() does not contain %+v", lookup)
	}

	if err := s.RestoreFinding(ctx, ref); err != nil {
		t.Fatalf("RestoreFinding() error = %v", err)
	}
	ignored, err = s.IgnoredFindings(ctx)
	if err != nil {
		t.Fatalf("IgnoredFindings() after restore error = %v", err)
	}
	if len(ignored) != 0 {
		t.Errorf("len(IgnoredFindings()) after restore = %d, want 0", len(ignored))
	}
}
