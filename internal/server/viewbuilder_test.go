package server

import (
	"strings"
	"testing"
	"time"

	"github.com/daknoblo/CFTM/internal/store"
)

func TestBuildHeartbeats(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	from := now.Add(-24 * time.Hour)

	// Healthy all day except a short outage two hours before the window ends.
	changes := []store.StatusChange{
		{Status: "down", ChangedAt: now.Add(-2 * time.Hour)},
		{Status: "healthy", ChangedAt: now.Add(-90 * time.Minute)},
	}

	beats := buildHeartbeats("healthy", changes, from, now, 48)
	if len(beats) != 48 {
		t.Fatalf("len(beats) = %d, want 48", len(beats))
	}

	var down int
	for _, b := range beats {
		if b.Status == "down" {
			down++
		}
	}
	// A 30-minute outage lands in one bucket; the transition back may touch a second.
	if down < 1 || down > 2 {
		t.Errorf("down buckets = %d, want 1 or 2", down)
	}
	if beats[0].Status != "healthy" {
		t.Errorf("first bucket = %q, want healthy carried in from the window start", beats[0].Status)
	}
	if beats[len(beats)-1].Status != "healthy" {
		t.Errorf("last bucket = %q, want healthy", beats[len(beats)-1].Status)
	}
}

func TestBuildHeartbeatsWithoutData(t *testing.T) {
	now := time.Now()
	beats := buildHeartbeats("", nil, now.Add(-24*time.Hour), now, 12)

	if len(beats) != 12 {
		t.Fatalf("len(beats) = %d, want 12", len(beats))
	}
	for i, b := range beats {
		if b.Status != "" {
			t.Errorf("beats[%d].Status = %q, want empty", i, b.Status)
		}
	}
}

func TestBuildHeartbeatsPrefersTheWorstStatusInABucket(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	from := now.Add(-time.Hour)

	// Two transitions inside the same bucket: the outage must win.
	changes := []store.StatusChange{
		{Status: "down", ChangedAt: from.Add(5 * time.Minute)},
		{Status: "healthy", ChangedAt: from.Add(10 * time.Minute)},
	}

	beats := buildHeartbeats("healthy", changes, from, now, 2)
	if len(beats) != 2 {
		t.Fatalf("len(beats) = %d, want 2", len(beats))
	}
	if beats[0].Status != "down" {
		t.Errorf("beats[0].Status = %q, want down", beats[0].Status)
	}
	if beats[1].Status != "healthy" {
		t.Errorf("beats[1].Status = %q, want healthy", beats[1].Status)
	}
}

func TestBuildHeartbeatsRejectsInvalidRange(t *testing.T) {
	now := time.Now()
	if got := buildHeartbeats("healthy", nil, now, now, 10); got != nil {
		t.Errorf("buildHeartbeats() = %v, want nil for an empty range", got)
	}
	if got := buildHeartbeats("healthy", nil, now.Add(-time.Hour), now, 0); got != nil {
		t.Errorf("buildHeartbeats() = %v, want nil for zero buckets", got)
	}
}

func TestServiceKind(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:8080":  "http",
		"https://127.0.0.1:8971": "http",
		"ssh://localhost:22":     "ssh",
		"rdp://localhost:3389":   "rdp",
		"tcp://localhost:5432":   "tcp",
		"http_status:404":        "catch-all",
		"unix:/var/run/x.sock":   "other",
	}
	for service, want := range cases {
		if got := serviceKind(service); got != want {
			t.Errorf("serviceKind(%q) = %q, want %q", service, got, want)
		}
	}
}

func TestActivePathCollapsesTunnelDetail(t *testing.T) {
	cases := map[string]string{
		"/":            "/",
		"/tunnels/abc": "/",
		"/ingress":     "/ingress",
		"/audit":       "/audit",
	}
	for path, want := range cases {
		if got := activePath(path); got != want {
			t.Errorf("activePath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestSanitizeLogValue(t *testing.T) {
	got := sanitizeLogValue("/path\nINFO forged log line\r\t")
	for _, bad := range []string{"\n", "\r", "\t"} {
		if strings.Contains(got, bad) {
			t.Errorf("sanitizeLogValue() = %q, still contains a control character", got)
		}
	}
}
