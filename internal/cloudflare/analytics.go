package cloudflare

import (
	"context"
	"fmt"
	"time"
)

// maxAnalyticsGroups caps one analytics query. The point is a country summary,
// not a copy of the request log, and the cardinality is country times hostname
// times path times status.
const maxAnalyticsGroups = 500

// OriginGroup is one aggregated bucket of HTTP requests by origin country.
type OriginGroup struct {
	Country  string
	Hostname string
	Path     string
	Status   int
	Requests int
	// Sampled marks an estimate rather than a true count, which the adaptive
	// datasets return once the volume grows.
	Sampled bool
}

// LoginGroup is one aggregated bucket of Access login attempts by country.
type LoginGroup struct {
	Country   string
	Succeeded bool
	Attempts  int
}

// AnalyticsLimits are the plan-dependent bounds of one dataset.
type AnalyticsLimits struct {
	// MaxDuration is the widest window a single query may span.
	MaxDuration time.Duration
	// NotOlderThan is how far back the dataset reaches.
	NotOlderThan time.Duration
	MaxPageSize  int
}

// Known reports whether the API supplied any bounds.
func (l AnalyticsLimits) Known() bool { return l.NotOlderThan > 0 || l.MaxDuration > 0 }

// The filters are written inline rather than as typed input objects: the
// generated type names differ per dataset and are not worth guessing.
var (
	httpRequestsByCountryQuery = fmt.Sprintf(`query($zoneTag: string, $start: string, $end: string) {
	  viewer { zones(filter: {zoneTag: $zoneTag}) {
	    httpRequestsAdaptiveGroups(
	      limit: %d
	      filter: {datetime_geq: $start, datetime_leq: $end}
	      orderBy: [count_DESC]
	    ) {
	      count
	      avg { sampleInterval }
	      dimensions { clientCountryName clientRequestHTTPHost clientRequestPath edgeResponseStatus }
	    }
	  } }
	}`, maxAnalyticsGroups)

	accessLoginsByCountryQuery = fmt.Sprintf(`query($accountTag: string, $start: string, $end: string) {
	  viewer { accounts(filter: {accountTag: $accountTag}) {
	    accessLoginRequestsAdaptiveGroups(
	      limit: %d
	      filter: {datetime_geq: $start, datetime_leq: $end}
	      orderBy: [count_DESC]
	    ) {
	      count
	      dimensions { country isSuccessfulLogin }
	    }
	  } }
	}`, maxAnalyticsGroups)

	httpRequestsLimitsQuery = `query($zoneTag: string) {
	  viewer { zones(filter: {zoneTag: $zoneTag}) {
	    settings { httpRequestsAdaptiveGroups { maxDuration maxPageSize notOlderThan } }
	  } }
	}`
)

// HTTPRequestsByCountry aggregates proxied requests of one zone by origin
// country. It is the only source that also sees traffic an Access bypass
// policy waves through, because bypass is never logged as an Access event.
func (c *Client) HTTPRequestsByCountry(ctx context.Context, zoneTag string, since, until time.Time) ([]OriginGroup, error) {
	type group struct {
		Count int `json:"count"`
		Avg   struct {
			SampleInterval float64 `json:"sampleInterval"`
		} `json:"avg"`
		Dimensions struct {
			Country  string `json:"clientCountryName"`
			Hostname string `json:"clientRequestHTTPHost"`
			Path     string `json:"clientRequestPath"`
			Status   int    `json:"edgeResponseStatus"`
		} `json:"dimensions"`
	}
	type data struct {
		Viewer struct {
			Zones []struct {
				Groups []group `json:"httpRequestsAdaptiveGroups"`
			} `json:"zones"`
		} `json:"viewer"`
	}

	out, err := graphQL[data](ctx, c, httpRequestsByCountryQuery, map[string]any{
		"zoneTag": zoneTag,
		"start":   since.UTC().Format(time.RFC3339),
		"end":     until.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return nil, err
	}

	var groups []OriginGroup
	for _, zone := range out.Viewer.Zones {
		for _, g := range zone.Groups {
			groups = append(groups, OriginGroup{
				Country:  g.Dimensions.Country,
				Hostname: g.Dimensions.Hostname,
				Path:     g.Dimensions.Path,
				Status:   g.Dimensions.Status,
				Requests: g.Count,
				Sampled:  g.Avg.SampleInterval > 1,
			})
		}
	}
	return groups, nil
}

// AccessLoginsByCountry aggregates Access login attempts by origin country.
// Unlike the REST audit log this also covers non-identity attempts, which is
// what a country policy produces.
func (c *Client) AccessLoginsByCountry(ctx context.Context, since, until time.Time) ([]LoginGroup, error) {
	type group struct {
		Count      int `json:"count"`
		Dimensions struct {
			Country string `json:"country"`
			Success int    `json:"isSuccessfulLogin"`
		} `json:"dimensions"`
	}
	type data struct {
		Viewer struct {
			Accounts []struct {
				Groups []group `json:"accessLoginRequestsAdaptiveGroups"`
			} `json:"accounts"`
		} `json:"viewer"`
	}

	out, err := graphQL[data](ctx, c, accessLoginsByCountryQuery, map[string]any{
		"accountTag": c.accountID,
		"start":      since.UTC().Format(time.RFC3339),
		"end":        until.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return nil, err
	}

	var groups []LoginGroup
	for _, account := range out.Viewer.Accounts {
		for _, g := range account.Groups {
			groups = append(groups, LoginGroup{
				Country:   g.Dimensions.Country,
				Succeeded: g.Dimensions.Success == 1,
				Attempts:  g.Count,
			})
		}
	}
	return groups, nil
}

// HTTPRequestsLimits reports how far back the zone may be queried. The bounds
// depend on the plan, so they are read rather than assumed.
func (c *Client) HTTPRequestsLimits(ctx context.Context, zoneTag string) (AnalyticsLimits, error) {
	type data struct {
		Viewer struct {
			Zones []struct {
				Settings struct {
					HTTPRequests struct {
						MaxDuration  int `json:"maxDuration"`
						MaxPageSize  int `json:"maxPageSize"`
						NotOlderThan int `json:"notOlderThan"`
					} `json:"httpRequestsAdaptiveGroups"`
				} `json:"settings"`
			} `json:"zones"`
		} `json:"viewer"`
	}

	out, err := graphQL[data](ctx, c, httpRequestsLimitsQuery, map[string]any{"zoneTag": zoneTag})
	if err != nil {
		return AnalyticsLimits{}, err
	}
	if len(out.Viewer.Zones) == 0 {
		return AnalyticsLimits{}, nil
	}

	s := out.Viewer.Zones[0].Settings.HTTPRequests
	return AnalyticsLimits{
		MaxDuration:  time.Duration(s.MaxDuration) * time.Second,
		NotOlderThan: time.Duration(s.NotOlderThan) * time.Second,
		MaxPageSize:  s.MaxPageSize,
	}, nil
}

// ClampWindow shortens a requested window to what the plan allows, so a
// generous setting degrades instead of making every query fail.
func (l AnalyticsLimits) ClampWindow(window time.Duration) time.Duration {
	for _, bound := range []time.Duration{l.NotOlderThan, l.MaxDuration} {
		if bound > 0 && window > bound {
			window = bound
		}
	}
	return window
}
