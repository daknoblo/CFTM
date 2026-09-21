package store

import (
	"testing"
	"time"
)

func TestProbeHistoryScopeAndOrder(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	since := now.Add(-24 * time.Hour)
	results := []ProbeResult{
		{Hostname: "a.example.com", CheckedAt: now, Class: "ok", LatencyMS: 30},
		{Hostname: "b.example.com", CheckedAt: now, Class: "ok", LatencyMS: 999},
		{Hostname: "a.example.com", CheckedAt: since, Class: "ok", LatencyMS: 999},
		{Hostname: "a.example.com", CheckedAt: now.Add(time.Second), Class: "ok", LatencyMS: 999},
		{Hostname: "a.example.com", CheckedAt: since.Add(time.Second), Class: "timeout", LatencyMS: 10000},
		{Hostname: "a.example.com", CheckedAt: now, Class: "edge_cached", LatencyMS: 40},
	}
	if err := s.AddProbeResults(t.Context(), results); err != nil {
		t.Fatal(err)
	}
	got, err := s.ProbeHistory(t.Context(), "a.example.com", since, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d samples, want 3", len(got))
	}
	for i, want := range []int{10000, 30, 40} {
		if got[i].LatencyMS != want {
			t.Errorf("sample %d: got %d ms, want %d", i, got[i].LatencyMS, want)
		}
	}
	empty, err := s.ProbeHistory(t.Context(), "' OR 1=1 --", since, now)
	if err != nil || len(empty) != 0 {
		t.Errorf("unknown hostname: got %v, %v, want no samples and no error", empty, err)
	}
}
