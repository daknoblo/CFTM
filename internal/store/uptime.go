package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// statusHealthy mirrors the Cloudflare status value counted as available.
const statusHealthy = "healthy"

// StatusHistory returns the transitions of a tunnel that happened at or after
// since, oldest first.
func (s *Store) StatusHistory(ctx context.Context, tunnelID string, since time.Time) ([]StatusChange, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT tunnel_id, from_status, status, changed_at
		 FROM tunnel_status_history
		 WHERE tunnel_id = ? AND changed_at >= ?
		 ORDER BY changed_at, id`, tunnelID, toUnix(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StatusChange
	for rows.Next() {
		var (
			c         StatusChange
			changedAt int64
		)
		if err := rows.Scan(&c.TunnelID, &c.FromStatus, &c.Status, &changedAt); err != nil {
			return nil, err
		}
		c.ChangedAt = fromUnix(changedAt)
		out = append(out, c)
	}
	return out, rows.Err()
}

// StatusAt returns the status in effect at t, that is the most recent
// transition at or before it.
func (s *Store) StatusAt(ctx context.Context, tunnelID string, t time.Time) (string, bool, error) {
	var status string
	err := s.db.QueryRowContext(ctx,
		`SELECT status FROM tunnel_status_history
		 WHERE tunnel_id = ? AND changed_at <= ?
		 ORDER BY changed_at DESC, id DESC LIMIT 1`, tunnelID, toUnix(t)).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return status, true, nil
}

// UptimeFor computes the share of the window a tunnel spent healthy.
//
// The window is clamped to the first observation, so a tunnel that has only
// been monitored for minutes does not report a near-zero 30-day uptime.
func (s *Store) UptimeFor(ctx context.Context, tunnelID string, window time.Duration, now time.Time) (Uptime, error) {
	windowStart := now.Add(-window)

	changes, err := s.StatusHistory(ctx, tunnelID, windowStart)
	if err != nil {
		return Uptime{}, err
	}
	status, known, err := s.StatusAt(ctx, tunnelID, windowStart)
	if err != nil {
		return Uptime{}, err
	}

	effectiveStart := windowStart
	current := status
	if known {
		// statusAt already consumed every transition up to and including the
		// window start; keeping them would double-count the boundary.
		for len(changes) > 0 && !changes[0].ChangedAt.After(windowStart) {
			changes = changes[1:]
		}
	} else {
		if len(changes) == 0 {
			return Uptime{Window: window}, nil
		}
		effectiveStart = changes[0].ChangedAt
		current = changes[0].Status
		changes = changes[1:]
	}

	var healthy time.Duration
	addHealthy := func(from, until time.Time) {
		if current != statusHealthy {
			return
		}
		if d := until.Sub(from); d > 0 {
			healthy += d
		}
	}

	cursor := effectiveStart
	transitions := 0
	for _, c := range changes {
		if c.ChangedAt.After(now) {
			break
		}
		addHealthy(cursor, c.ChangedAt)
		cursor = c.ChangedAt
		current = c.Status
		transitions++
	}
	addHealthy(cursor, now)

	observed := now.Sub(effectiveStart)
	if observed <= 0 {
		return Uptime{Observed: true, Changes: transitions}, nil
	}

	return Uptime{
		Window:       observed,
		HealthyRatio: float64(healthy) / float64(observed),
		Changes:      transitions,
		Observed:     true,
	}, nil
}
