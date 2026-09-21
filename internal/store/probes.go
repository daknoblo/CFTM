package store

import (
	"context"
	"time"
)

// ProbeHistory returns one hostname's samples in (since, until], oldest first.
func (s *Store) ProbeHistory(ctx context.Context, hostname string, since, until time.Time) ([]ProbeResult, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT hostname, checked_at, status_code, class, latency_ms
		 FROM probe_results
		 WHERE hostname = ? AND checked_at > ? AND checked_at <= ?
		 ORDER BY checked_at, id`, hostname, toUnix(since), toUnix(until))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ProbeResult
	for rows.Next() {
		var r ProbeResult
		var checkedAt int64
		if err := rows.Scan(&r.Hostname, &checkedAt, &r.StatusCode, &r.Class, &r.LatencyMS); err != nil {
			return nil, err
		}
		r.CheckedAt = fromUnix(checkedAt)
		out = append(out, r)
	}
	return out, rows.Err()
}
