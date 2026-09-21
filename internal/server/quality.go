package server

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/daknoblo/CFTM/internal/prober"
	"github.com/daknoblo/CFTM/internal/store"
	"github.com/daknoblo/CFTM/internal/web"
)

const qualityBuckets = 288

var errQualityHostname = errors.New("hostname is not a probeable hostname of this tunnel")

func (s *Server) tunnelQuality(ctx context.Context, id, hostname string, rules []store.IngressRule, now time.Time) (web.TunnelQuality, error) {
	q := web.TunnelQuality{
		TunnelID: id, Enabled: s.cfg.ProbeEnabled, Interval: s.cfg.ProbeInterval.String(),
		Since: now.Truncate(time.Second).Add(-24 * time.Hour), Until: now.Truncate(time.Second),
	}
	for _, target := range prober.Targets(rules, nil) {
		q.Hostnames = append(q.Hostnames, target.Hostname)
	}
	sort.Strings(q.Hostnames)
	if hostname != "" {
		i := sort.SearchStrings(q.Hostnames, hostname)
		if i == len(q.Hostnames) || q.Hostnames[i] != hostname {
			return q, errQualityHostname
		}
		q.Hostname = hostname
	} else if len(q.Hostnames) > 0 {
		q.Hostname = q.Hostnames[0]
	}
	q.RefreshURL = "/partials/tunnels/" + url.PathEscape(id) + "/quality"
	if q.Hostname == "" {
		return q, nil
	}
	results, err := s.store.ProbeHistory(ctx, q.Hostname, q.Since, q.Until)
	if err != nil {
		return q, fmt.Errorf("load quality history: %w", err)
	}
	buildQuality(&q, results, s.cfg.ProbeInterval)
	return q, nil
}

func (s *Server) handlePartialQuality(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tunnels, err := s.store.Tunnels(r.Context())
	if err != nil {
		s.serverError(w, r, "loading tunnel", err)
		return
	}
	found := false
	for _, t := range tunnels {
		found = found || t.ID == id
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	rules, err := s.store.Ingress(r.Context(), id)
	if err != nil {
		s.serverError(w, r, "loading ingress", err)
		return
	}
	q, err := s.tunnelQuality(r.Context(), id, r.URL.Query().Get("hostname"), rules, time.Now())
	if errors.Is(err, errQualityHostname) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		s.serverError(w, r, "building quality chart", err)
		return
	}
	w.Header().Set("HX-Replace-Url", "/tunnels/"+url.PathEscape(id)+"?hostname="+url.QueryEscape(q.Hostname))
	s.render(w, r, web.TunnelQualityCard(q))
}

type qualityBucket struct {
	count, failures, excluded, pairs int
	sum, low, high, variation        float64
	first, last                      time.Time
	broken                           bool
}

func buildQuality(q *web.TunnelQuality, results []store.ProbeResult, interval time.Duration) {
	var buckets [qualityBuckets]qualityBucket
	var latencies []float64
	var previous store.ProbeResult
	var variation float64
	pairs := 0
	maxGap := interval + interval/2
	for _, r := range results {
		if !r.CheckedAt.After(q.Since) || r.CheckedAt.After(q.Until) {
			continue
		}
		index := min(int(r.CheckedAt.Sub(q.Since)/(5*time.Minute)), qualityBuckets-1)
		b := &buckets[index]
		q.Samples++
		q.LastSample = r.CheckedAt
		if r.Class != prober.ClassOK {
			if prober.IsFailure(r.Class) {
				q.Failures++
				b.failures++
			} else {
				q.Excluded++
				b.excluded++
			}
			b.broken = true
			previous = store.ProbeResult{}
			continue
		}
		ms := float64(r.LatencyMS)
		latencies = append(latencies, ms)
		q.Successful++
		if b.count == 0 {
			b.first, b.low = r.CheckedAt, ms
		}
		b.last = r.CheckedAt
		b.count++
		b.sum += ms
		b.low = min(b.low, ms)
		b.high = max(b.high, ms)
		if previous.Class == prober.ClassOK {
			gap := r.CheckedAt.Sub(previous.CheckedAt)
			if gap > 0 && gap <= maxGap {
				delta := math.Abs(ms - float64(previous.LatencyMS))
				variation += delta
				pairs++
				b.variation += delta
				b.pairs++
			} else if b.count > 1 {
				b.broken = true
			}
		}
		previous = r
	}
	q.Stale = !q.LastSample.IsZero() && q.Until.Sub(q.LastSample) > maxGap
	if len(latencies) > 0 {
		sort.Float64s(latencies)
		n := len(latencies)
		median := (latencies[(n-1)/2] + latencies[n/2]) / 2
		p95 := latencies[int(math.Ceil(float64(n)*0.95))-1]
		q.MedianMS, q.P95MS = &median, &p95
	}
	if pairs > 0 {
		mean := variation / float64(pairs)
		q.VariationMS = &mean
	}
	if measured := q.Successful + q.Failures; measured > 0 {
		percent := 100 * float64(q.Failures) / float64(measured)
		q.FailurePercent = &percent
	}
	q.Chart = qualityGeometry(buckets, maxGap, q.Since)
}

func qualityGeometry(buckets [qualityBuckets]qualityBucket, maxGap time.Duration, since time.Time) web.QualityChart {
	chart := web.QualityChart{MaxMS: 1}
	for _, b := range buckets {
		chart.MaxMS = max(chart.MaxMS, b.high)
		if b.pairs > 0 {
			chart.MaxMS = max(chart.MaxMS, b.variation/float64(b.pairs))
		}
	}
	chart.MaxMS = math.Ceil(chart.MaxMS/10) * 10
	y := func(ms float64) float64 { return 190 - 170*ms/chart.MaxMS }
	var latencyPath, variationPath strings.Builder
	var previous qualityBucket
	var previousX, previousY, previousVariationY float64
	for i, b := range buckets {
		if b.count+b.failures+b.excluded == 0 {
			previous = qualityBucket{}
			continue
		}
		x := 50 + (float64(i)+0.5)*900/qualityBuckets
		start := since.Add(time.Duration(i) * 5 * time.Minute)
		p := web.QualityPoint{
			X:     fmt.Sprintf("%.2f", x),
			HitX:  fmt.Sprintf("%.4f", 50+float64(i)*900/qualityBuckets),
			Since: start, Until: start.Add(5 * time.Minute),
			HasLatency: b.count > 0, HasVariation: b.pairs > 0,
			Failures: b.failures, Excluded: b.excluded,
			Label:    fmt.Sprintf("%d responses / %d failed / %d excluded", b.count, b.failures, b.excluded),
			Response: "Response: unavailable", Variation: "Variation: unavailable",
		}
		connect := previous.count > 0 && !previous.broken && !b.broken && b.first.Sub(previous.last) <= maxGap
		if p.HasLatency {
			mean := b.sum / float64(b.count)
			p.Y, p.LowY, p.HighY = fmt.Sprintf("%.2f", y(mean)), fmt.Sprintf("%.2f", y(b.low)), fmt.Sprintf("%.2f", y(b.high))
			p.Response = fmt.Sprintf("Response: %.1f ms (min %.1f / max %.1f)", mean, b.low, b.high)
			appendQualityCurve(&latencyPath, previousX, previousY, x, y(mean), connect)
			previousY = y(mean)
		}
		if p.HasVariation {
			ms := b.variation / float64(b.pairs)
			p.VariationY = fmt.Sprintf("%.2f", y(ms))
			p.Variation = fmt.Sprintf("Variation: %.1f ms", ms)
			appendQualityCurve(&variationPath, previousX, previousVariationY, x, y(ms), connect && previous.pairs > 0)
			previousVariationY = y(ms)
		}
		chart.Points = append(chart.Points, p)
		previous = b
		previousX = x
	}
	chart.LatencyPath, chart.VariationPath = latencyPath.String(), variationPath.String()
	return chart
}

func appendQualityCurve(path *strings.Builder, fromX, fromY, x, y float64, connect bool) {
	if !connect {
		fmt.Fprintf(path, "M%.2f,%.2f ", x, y)
		return
	}
	// Horizontal handles round corners without overshooting either measured value.
	handle := (x - fromX) * 0.2
	fmt.Fprintf(path, "C%.2f,%.2f %.2f,%.2f %.2f,%.2f ", fromX+handle, fromY, x-handle, y, x, y)
}
