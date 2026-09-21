package server

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/daknoblo/CFTM/internal/prober"
	"github.com/daknoblo/CFTM/internal/store"
	"github.com/daknoblo/CFTM/internal/web"
)

func TestQualityStatistics(t *testing.T) {
	since := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	q := web.TunnelQuality{Since: since, Until: since.Add(24 * time.Hour)}
	sample := func(minute, ms int, class string) store.ProbeResult {
		return store.ProbeResult{CheckedAt: since.Add(time.Duration(minute) * time.Minute), LatencyMS: ms, Class: class}
	}
	buildQuality(&q, []store.ProbeResult{
		sample(0, 99999, prober.ClassOK),
		sample(1, 100, prober.ClassOK),
		sample(6, 200, prober.ClassOK),
		sample(11, 10000, prober.ClassTimeout),
		sample(16, 300, prober.ClassOK),
		sample(21, 10, prober.ClassAccessChallenge),
		sample(26, 20, prober.ClassEdgeCached),
		sample(31, 400, prober.ClassOK),
		sample(46, 500, prober.ClassOK),
		sample(51, 700, prober.ClassOK),
		sample(1441, 99999, prober.ClassOK),
	}, 5*time.Minute)
	if q.Samples != 9 || q.Successful != 6 || q.Failures != 1 || q.Excluded != 2 {
		t.Fatalf("got counts %d/%d/%d/%d, want 9/6/1/2", q.Samples, q.Successful, q.Failures, q.Excluded)
	}
	for _, tc := range []struct {
		name string
		got  *float64
		want float64
	}{
		{"median", q.MedianMS, 350},
		{"p95", q.P95MS, 700},
		{"variation", q.VariationMS, 150},
		{"failure percentage", q.FailurePercent, 100.0 / 7},
	} {
		if tc.got == nil || math.Abs(*tc.got-tc.want) > 0.0001 {
			t.Errorf("%s: got %v, want %v", tc.name, tc.got, tc.want)
		}
	}
	if q.Chart.MaxMS != 700 {
		t.Errorf("got scale %v, want 700 (timeouts and excluded samples must not affect it)", q.Chart.MaxMS)
	}
	if got := strings.Count(q.Chart.LatencyPath, "M"); got != 4 {
		t.Errorf("got %d disconnected segments, want 4", got)
	}
	if !q.Stale {
		t.Error("got fresh measurements, want stale")
	}
}

func TestQualityEmptyAndSingleSamples(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	for _, tc := range []struct {
		name    string
		results []store.ProbeResult
		latency bool
		failure bool
	}{
		{"empty", nil, false, false},
		{"edge only", []store.ProbeResult{{CheckedAt: now, Class: prober.ClassAccessDenied}}, false, false},
		{"failure only", []store.ProbeResult{{CheckedAt: now, Class: prober.ClassTimeout, LatencyMS: 10000}}, false, true},
		{"zero latency", []store.ProbeResult{{CheckedAt: now, Class: prober.ClassOK}}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := web.TunnelQuality{Since: now.Add(-24 * time.Hour), Until: now}
			buildQuality(&q, tc.results, time.Minute)
			if (q.MedianMS != nil) != tc.latency || (q.P95MS != nil) != tc.latency || (q.FailurePercent != nil) != tc.failure {
				t.Errorf("got stats %+v, want latency=%v failure=%v", q, tc.latency, tc.failure)
			}
			if q.VariationMS != nil || q.Stale {
				t.Errorf("got variation=%v stale=%v, want nil/false", q.VariationMS, q.Stale)
			}
		})
	}
}

func TestQualityBucketsBoundOutputAndPreserveSpikes(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	q := web.TunnelQuality{Since: now.Add(-24 * time.Hour), Until: now}
	var results []store.ProbeResult
	for i := 1; i <= 24*60*60; i++ {
		ms := 10
		if i == 100 {
			ms = 1500
		}
		results = append(results, store.ProbeResult{CheckedAt: q.Since.Add(time.Duration(i) * time.Second), Class: prober.ClassOK, LatencyMS: ms})
	}
	buildQuality(&q, results, time.Second)
	if len(q.Chart.Points) != qualityBuckets || q.Samples != 86400 {
		t.Errorf("got points=%d samples=%d, want 288/86400", len(q.Chart.Points), q.Samples)
	}
	if q.Chart.MaxMS != 1500 || q.Chart.Points[0].HighY != "20.00" {
		t.Errorf("got scale=%v peak=%s, want 1500/20.00", q.Chart.MaxMS, q.Chart.Points[0].HighY)
	}
	if q.P95MS == nil || *q.P95MS != 10 {
		t.Errorf("got p95=%v, want 10", q.P95MS)
	}
}

func TestQualityDoesNotConnectAcrossWithinBucketFailures(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	q := web.TunnelQuality{Since: now.Add(-24 * time.Hour), Until: now}
	var samples []store.ProbeResult
	for i, minute := range []int{1, 6, 7, 8, 11} {
		class := prober.ClassOK
		if i == 2 {
			class = prober.ClassTimeout
		}
		samples = append(samples, store.ProbeResult{CheckedAt: q.Since.Add(time.Duration(minute) * time.Minute), Class: class, LatencyMS: 10})
	}
	buildQuality(&q, samples, 5*time.Minute)
	if strings.ContainsAny(q.Chart.LatencyPath, "LC") {
		t.Errorf("got path %q, want no lines across interrupted bucket", q.Chart.LatencyPath)
	}
}

func TestQualityHTTPAndHostSelection(t *testing.T) {
	srv, h := newTestServer(t)
	srv.cfg.ProbeEnabled = true
	now := time.Now().Add(-time.Second)
	if err := srv.store.AddProbeResults(t.Context(), []store.ProbeResult{
		{Hostname: "app.example.com", CheckedAt: now.Add(-5 * time.Minute), Class: prober.ClassOK, LatencyMS: 100},
		{Hostname: "app.example.com", CheckedAt: now, Class: prober.ClassOK, LatencyMS: 200},
		{Hostname: "public-app.example.com", CheckedAt: now, Class: prober.ClassOK, LatencyMS: 900},
		{Hostname: "foreign.example.com", CheckedAt: now, Class: prober.ClassOK, LatencyMS: 9999},
	}); err != nil {
		t.Fatal(err)
	}
	rec := get(t, h, "/tunnels/t1")
	body := rec.Body.String()
	for _, want := range []string{
		"Response time &amp; stability", "150.0 ms", "100.0 ms",
		`hx-trigger="every 10s, change"`, `hx-include="this"`, `hx-sync="this:replace"`,
		"quality-host-form", "data-quality-error", "data-quality-grid", "data-quality-point",
		"data-quality-tooltip", "data-response=", "data-since=", "data-until=",
		`datetime="` + web.FormatTime(now.Truncate(time.Second)) + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("got body missing %q, want quality panel content", want)
		}
	}
	for _, unwanted := range []string{">Show</button>", "@timestamp(", "Five-minute buckets; whiskers preserve spikes."} {
		if strings.Contains(body, unwanted) {
			t.Errorf("got obsolete markup %q, want simplified chart", unwanted)
		}
	}
	if strings.Index(body, `id="tunnel-quality"`) < strings.Index(body, "One segment per half hour") {
		t.Error("got quality panel before uptime, want after")
	}
	for _, path := range []string{"/tunnels/t1", "/partials/tunnels/t1/quality"} {
		rec = get(t, h, path+"?hostname=public-app.example.com")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "900.0 ms") {
			t.Errorf("GET %s: got %d, want 200 with selected hostname data", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "150.0 ms") {
			t.Errorf("GET %s: got other hostname's median, want isolated history", path)
		}
		if strings.Contains(rec.Body.String(), sentinelToken) {
			t.Errorf("GET %s: got API credential in response, want no secrets", path)
		}
		if strings.HasPrefix(path, "/partials/") {
			if got := rec.Header().Get("HX-Replace-Url"); got != "/tunnels/t1?hostname=public-app.example.com" {
				t.Errorf("got replacement URL %q, want selected hostname on tunnel page", got)
			}
		}
	}
	for _, path := range []string{"/tunnels/t1", "/api/tunnels/t1", "/partials/tunnels/t1/quality"} {
		for _, hostname := range []string{"foreign.example.com", "ssh.example.com", "missing.example.com"} {
			if rec := get(t, h, path+"?hostname="+hostname); rec.Code != http.StatusBadRequest {
				t.Errorf("GET %s hostname %s: got %d, want 400", path, hostname, rec.Code)
			}
		}
	}
	if rec := get(t, h, "/partials/tunnels/missing/quality"); rec.Code != http.StatusNotFound {
		t.Errorf("got missing tunnel status %d, want 404", rec.Code)
	}
	var payload web.TunnelDetail
	rec = get(t, h, "/api/tunnels/t1?hostname=public-app.example.com")
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Quality.MedianMS == nil || *payload.Quality.MedianMS != 900 || payload.Quality.Hostname != "public-app.example.com" {
		t.Errorf("got API quality %+v, want selected hostname median 900", payload.Quality)
	}
}

func TestQualityUnavailableStatesAndErrors(t *testing.T) {
	srv, h := newTestServer(t)
	rec := get(t, h, "/partials/tunnels/t1/quality")
	for _, want := range []string{"Probing is disabled", "No samples in the last 24 hours"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("got body missing %q, want unavailable explanation", want)
		}
	}
	q, err := srv.tunnelQuality(t.Context(), "t1", "", []store.IngressRule{
		{Hostname: "*.example.com", Service: "http://localhost"},
		{Hostname: "ssh.example.com", Service: "ssh://localhost"},
	}, time.Now())
	if err != nil || len(q.Hostnames) != 0 {
		t.Errorf("got hosts=%v err=%v, want no probeable hosts and no error", q.Hostnames, err)
	}
	if err := srv.store.Close(); err != nil {
		t.Fatal(err)
	}
	if rec := get(t, h, "/partials/tunnels/t1/quality"); rec.Code != http.StatusInternalServerError {
		t.Errorf("got database failure status %d, want 500", rec.Code)
	}
}

func TestQualityCurvePreservesValues(t *testing.T) {
	for _, ys := range [][2]float64{{190, 20}, {20, 190}, {105, 105}} {
		var path strings.Builder
		appendQualityCurve(&path, 50, ys[0], 100, ys[1], true)
		var x1, y1, x2, y2, x, y float64
		if n, err := fmt.Sscanf(path.String(), "C%f,%f %f,%f %f,%f", &x1, &y1, &x2, &y2, &x, &y); err != nil || n != 6 {
			t.Fatalf("got path %q, want a cubic curve: %v", path.String(), err)
		}
		if x1 != 60 || x2 != 90 || x != 100 || y1 != ys[0] || y2 != ys[1] || y != ys[1] {
			t.Fatalf("got curve %q, want horizontal handles within the measured endpoints", path.String())
		}
		for i := range 101 {
			at := float64(i) / 100
			inverse := 1 - at
			value := inverse*inverse*inverse*ys[0] + 3*inverse*inverse*at*y1 + 3*inverse*at*at*y2 + at*at*at*y
			if value < min(ys[0], ys[1])-0.0001 || value > max(ys[0], ys[1])+0.0001 {
				t.Errorf("got interpolated value %v, want no overshoot between %v", value, ys)
			}
		}
	}
	var gap strings.Builder
	appendQualityCurve(&gap, 50, 190, 100, 20, false)
	if got := gap.String(); got != "M100.00,20.00 " {
		t.Errorf("got gap path %q, want move without interpolation", got)
	}
}

func TestQualityTooltipBucketValues(t *testing.T) {
	since := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	q := web.TunnelQuality{Since: since, Until: since.Add(24 * time.Hour)}
	buildQuality(&q, []store.ProbeResult{
		{CheckedAt: since.Add(time.Minute), Class: prober.ClassOK, LatencyMS: 10},
		{CheckedAt: since.Add(2 * time.Minute), Class: prober.ClassOK, LatencyMS: 30},
		{CheckedAt: since.Add(6 * time.Minute), Class: prober.ClassTimeout, LatencyMS: 10000},
	}, time.Minute)
	if len(q.Chart.Points) != 2 {
		t.Fatalf("got %d points, want 2", len(q.Chart.Points))
	}
	first, second := q.Chart.Points[0], q.Chart.Points[1]
	if !first.Since.Equal(since) || !first.Until.Equal(since.Add(5*time.Minute)) || first.HitX != "50.0000" {
		t.Errorf("got time range %v - %v, x=%s, want first five-minute bucket", first.Since, first.Until, first.HitX)
	}
	if first.Response != "Response: 20.0 ms (min 10.0 / max 30.0)" || first.Variation != "Variation: 20.0 ms" {
		t.Errorf("got tooltip %q / %q, want mean=20, min=10, max=30, variation=20", first.Response, first.Variation)
	}
	if second.Response != "Response: unavailable" || second.Variation != "Variation: unavailable" || second.Label != "0 responses / 1 failed / 0 excluded" {
		t.Errorf("got failure tooltip %+v, want failure without timeout latency", second)
	}
}
