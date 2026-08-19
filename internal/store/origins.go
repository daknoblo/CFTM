package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Sources a request origin can be observed through. They overlap only
// partially, so the origin they came from stays part of the key.
const (
	// OriginSourceHTTP is the zone-scoped HTTP analytics, the only source that
	// also sees traffic an Access bypass policy waves through.
	OriginSourceHTTP = "http"
	// OriginSourceLogin is the Access login analytics, which covers identity
	// and non-identity attempts but nothing a bypass policy let through.
	OriginSourceLogin = "access_login"
	// OriginSourceAudit is the REST Access audit log, identity events only.
	OriginSourceAudit = "access_audit"
)

// UnknownCountry stands in where the edge reported no country.
const UnknownCountry = "XX"

// RequestOrigin is the request summary of one country for one hostname.
type RequestOrigin struct {
	Source   string    `json:"source"`
	Hostname string    `json:"hostname"`
	Path     string    `json:"path,omitempty"`
	Country  string    `json:"country"`
	Allowed  int       `json:"allowed"`
	Denied   int       `json:"denied"`
	Requests int       `json:"requests"`
	Sampled  bool      `json:"sampled"`
	Window   time.Time `json:"windowStart"`
	LastAt   time.Time `json:"lastAt"`
}

// ReplaceRequestOrigins swaps the whole summary for a freshly collected one.
func (s *Store) ReplaceRequestOrigins(ctx context.Context, origins []RequestOrigin) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM request_origins`); err != nil {
			return fmt.Errorf("store: clear request origins: %w", err)
		}
		for _, o := range origins {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO request_origins
				   (source, hostname, path, country, allowed, denied, requests, sampled, window_start, last_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				 ON CONFLICT(source, hostname, path, country) DO UPDATE SET
				   allowed  = allowed + excluded.allowed,
				   denied   = denied + excluded.denied,
				   requests = requests + excluded.requests,
				   sampled  = MAX(sampled, excluded.sampled),
				   last_at  = MAX(last_at, excluded.last_at)`,
				o.Source, o.Hostname, o.Path, o.Country,
				o.Allowed, o.Denied, o.Requests, boolToInt(o.Sampled),
				toUnix(o.Window), toUnix(o.LastAt),
			); err != nil {
				return fmt.Errorf("store: insert request origin: %w", err)
			}
		}
		return nil
	})
}

// RequestOrigins returns the stored summary, busiest country first.
func (s *Store) RequestOrigins(ctx context.Context) ([]RequestOrigin, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT source, hostname, path, country, allowed, denied, requests, sampled, window_start, last_at
		   FROM request_origins
		  ORDER BY requests DESC, country, hostname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RequestOrigin
	for rows.Next() {
		var (
			o                 RequestOrigin
			sampled           int
			windowStart, last int64
		)
		if err := rows.Scan(&o.Source, &o.Hostname, &o.Path, &o.Country,
			&o.Allowed, &o.Denied, &o.Requests, &sampled, &windowStart, &last); err != nil {
			return nil, err
		}
		o.Sampled = sampled != 0
		o.Window = fromUnix(windowStart)
		o.LastAt = fromUnix(last)
		out = append(out, o)
	}
	return out, rows.Err()
}

// OriginCountryTotals folds the summary down to one row per country.
func (s *Store) OriginCountryTotals(ctx context.Context) ([]RequestOrigin, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT country,
		        SUM(allowed), SUM(denied), SUM(requests),
		        MAX(sampled), MAX(last_at)
		   FROM request_origins
		  GROUP BY country
		  ORDER BY SUM(requests) DESC, country`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RequestOrigin
	for rows.Next() {
		var (
			o       RequestOrigin
			sampled int
			last    int64
		)
		if err := rows.Scan(&o.Country, &o.Allowed, &o.Denied, &o.Requests, &sampled, &last); err != nil {
			return nil, err
		}
		o.Sampled = sampled != 0
		o.LastAt = fromUnix(last)
		out = append(out, o)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
