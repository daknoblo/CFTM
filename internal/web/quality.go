package web

import "time"

// TunnelQuality describes HTTP observations, not ICMP or link-level telemetry.
type TunnelQuality struct {
	TunnelID       string       `json:"-"`
	RefreshURL     string       `json:"-"`
	Enabled        bool         `json:"enabled"`
	Interval       string       `json:"interval"`
	Hostnames      []string     `json:"hostnames"`
	Hostname       string       `json:"hostname"`
	Since          time.Time    `json:"since"`
	Until          time.Time    `json:"until"`
	LastSample     time.Time    `json:"lastSample,omitzero"`
	Stale          bool         `json:"stale"`
	Samples        int          `json:"samples"`
	Successful     int          `json:"successful"`
	Failures       int          `json:"failures"`
	Excluded       int          `json:"excluded"`
	MedianMS       *float64     `json:"medianMs"`
	P95MS          *float64     `json:"p95Ms"`
	VariationMS    *float64     `json:"variationMs"`
	FailurePercent *float64     `json:"failurePercent"`
	Chart          QualityChart `json:"-"`
}

// QualityChart keeps SVG geometry bounded to five-minute buckets.
type QualityChart struct {
	MaxMS         float64
	LatencyPath   string
	VariationPath string
	Points        []QualityPoint
}

type QualityPoint struct {
	X, Y, LowY, HighY, VariationY string
	HitX                          string
	Since, Until                  time.Time
	HasLatency, HasVariation      bool
	Failures, Excluded            int
	Label                         string
	Response, Variation           string
}
