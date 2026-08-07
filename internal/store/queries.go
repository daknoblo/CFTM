package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SaveTunnels writes a tunnel snapshot and returns what changed since the
// previous one. Status history rows are only appended on an actual transition.
func (s *Store) SaveTunnels(ctx context.Context, tunnels []Tunnel, now time.Time) (TunnelDiff, error) {
	var diff TunnelDiff

	previous, err := s.tunnelsByID(ctx)
	if err != nil {
		return diff, err
	}

	seen := make(map[string]bool, len(tunnels))
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		for _, t := range tunnels {
			seen[t.ID] = true
			prev, existed := previous[t.ID]

			firstSeen := now
			if existed {
				firstSeen = prev.FirstSeen
			} else {
				diff.Added = append(diff.Added, t)
			}

			if !existed || prev.Status != t.Status {
				change := StatusChange{
					TunnelID:   t.ID,
					FromStatus: prev.Status,
					Status:     t.Status,
					ChangedAt:  now,
				}
				diff.Changes = append(diff.Changes, change)
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO tunnel_status_history (tunnel_id, from_status, status, changed_at)
					 VALUES (?, ?, ?, ?)`,
					change.TunnelID, change.FromStatus, change.Status, toUnix(change.ChangedAt),
				); err != nil {
					return fmt.Errorf("store: insert status history: %w", err)
				}
			}

			if _, err := tx.ExecContext(ctx,
				`INSERT INTO tunnels (id, name, tun_type, config_src, remote_config, status,
					created_at, conns_active_at, conns_inactive_at, first_seen, last_seen)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				 ON CONFLICT(id) DO UPDATE SET
					name = excluded.name,
					tun_type = excluded.tun_type,
					config_src = excluded.config_src,
					remote_config = excluded.remote_config,
					status = excluded.status,
					created_at = excluded.created_at,
					conns_active_at = excluded.conns_active_at,
					conns_inactive_at = excluded.conns_inactive_at,
					last_seen = excluded.last_seen`,
				t.ID, t.Name, t.TunType, t.ConfigSrc, t.RemoteConfig, t.Status,
				toUnix(t.CreatedAt), toUnix(t.ConnsActiveAt), toUnix(t.ConnsInactiveAt),
				toUnix(firstSeen), toUnix(now),
			); err != nil {
				return fmt.Errorf("store: upsert tunnel: %w", err)
			}
		}

		for id, prev := range previous {
			if seen[id] {
				continue
			}
			diff.Removed = append(diff.Removed, prev)
			for _, stmt := range []string{
				`DELETE FROM tunnels WHERE id = ?`,
				`DELETE FROM connectors WHERE tunnel_id = ?`,
				`DELETE FROM connections WHERE tunnel_id = ?`,
				`DELETE FROM ingress WHERE tunnel_id = ?`,
			} {
				if _, err := tx.ExecContext(ctx, stmt, id); err != nil {
					return fmt.Errorf("store: remove tunnel: %w", err)
				}
			}
		}
		return nil
	})

	return diff, err
}

func (s *Store) tunnelsByID(ctx context.Context) (map[string]Tunnel, error) {
	tunnels, err := s.Tunnels(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Tunnel, len(tunnels))
	for _, t := range tunnels {
		out[t.ID] = t
	}
	return out, nil
}

// Tunnels returns every stored tunnel, ordered by name.
func (s *Store) Tunnels(ctx context.Context) ([]Tunnel, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, tun_type, config_src, remote_config, status,
			created_at, conns_active_at, conns_inactive_at, first_seen, last_seen
		 FROM tunnels ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Tunnel
	for rows.Next() {
		var (
			t                                                        Tunnel
			created, connsActive, connsInactive, firstSeen, lastSeen int64
		)
		if err := rows.Scan(&t.ID, &t.Name, &t.TunType, &t.ConfigSrc, &t.RemoteConfig, &t.Status,
			&created, &connsActive, &connsInactive, &firstSeen, &lastSeen); err != nil {
			return nil, err
		}
		t.CreatedAt = fromUnix(created)
		t.ConnsActiveAt = fromUnix(connsActive)
		t.ConnsInactiveAt = fromUnix(connsInactive)
		t.FirstSeen = fromUnix(firstSeen)
		t.LastSeen = fromUnix(lastSeen)
		out = append(out, t)
	}
	return out, rows.Err()
}

// Tunnel returns a single tunnel by ID.
func (s *Store) Tunnel(ctx context.Context, id string) (Tunnel, bool, error) {
	tunnels, err := s.Tunnels(ctx)
	if err != nil {
		return Tunnel{}, false, err
	}
	for _, t := range tunnels {
		if t.ID == id {
			return t, true, nil
		}
	}
	return Tunnel{}, false, nil
}

// ReplaceConnectors swaps the connector set of a tunnel and reports the delta.
func (s *Store) ReplaceConnectors(ctx context.Context, tunnelID string, connectors []Connector, now time.Time) (ConnectorDiff, error) {
	var diff ConnectorDiff

	previous, err := s.Connectors(ctx, tunnelID)
	if err != nil {
		return diff, err
	}
	prevByID := make(map[string]Connector, len(previous))
	for _, c := range previous {
		prevByID[c.ID] = c
	}

	incoming := make(map[string]bool, len(connectors))
	for _, c := range connectors {
		incoming[c.ID] = true
		prev, ok := prevByID[c.ID]
		if !ok {
			diff.Added = append(diff.Added, c)
			continue
		}
		if prev.Version != c.Version || prev.ConfigVersion != c.ConfigVersion {
			diff.Changed = append(diff.Changed, ConnectorChange{
				ID:                c.ID,
				FromVersion:       prev.Version,
				ToVersion:         c.Version,
				FromConfigVersion: prev.ConfigVersion,
				ToConfigVersion:   c.ConfigVersion,
			})
		}
	}
	for _, c := range previous {
		if !incoming[c.ID] {
			diff.Removed = append(diff.Removed, c)
		}
	}

	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM connectors WHERE tunnel_id = ?`, tunnelID); err != nil {
			return fmt.Errorf("store: clear connectors: %w", err)
		}
		for _, c := range connectors {
			features, err := json.Marshal(c.Features)
			if err != nil {
				return fmt.Errorf("store: encode features: %w", err)
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO connectors (id, tunnel_id, version, arch, config_version, features, run_at, last_seen)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				c.ID, tunnelID, c.Version, c.Arch, c.ConfigVersion, string(features),
				toUnix(c.RunAt), toUnix(now),
			); err != nil {
				return fmt.Errorf("store: insert connector: %w", err)
			}
		}
		return nil
	})

	return diff, err
}

// Connectors returns the connectors of a tunnel, or of every tunnel when
// tunnelID is empty.
func (s *Store) Connectors(ctx context.Context, tunnelID string) ([]Connector, error) {
	query := `SELECT id, tunnel_id, version, arch, config_version, features, run_at, last_seen
		FROM connectors`
	var args []any
	if tunnelID != "" {
		query += ` WHERE tunnel_id = ?`
		args = append(args, tunnelID)
	}
	query += ` ORDER BY tunnel_id, id`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Connector
	for rows.Next() {
		var (
			c               Connector
			features        string
			runAt, lastSeen int64
		)
		if err := rows.Scan(&c.ID, &c.TunnelID, &c.Version, &c.Arch, &c.ConfigVersion,
			&features, &runAt, &lastSeen); err != nil {
			return nil, err
		}
		if features != "" {
			_ = json.Unmarshal([]byte(features), &c.Features)
		}
		c.RunAt = fromUnix(runAt)
		c.LastSeen = fromUnix(lastSeen)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ReplaceConnections swaps the connection set of a tunnel.
func (s *Store) ReplaceConnections(ctx context.Context, tunnelID string, conns []Connection, now time.Time) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM connections WHERE tunnel_id = ?`, tunnelID); err != nil {
			return fmt.Errorf("store: clear connections: %w", err)
		}
		for _, c := range conns {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO connections (uuid, connector_id, tunnel_id, colo_name, origin_ip, opened_at, last_seen)
				 VALUES (?, ?, ?, ?, ?, ?, ?)
				 ON CONFLICT(uuid) DO UPDATE SET
					connector_id = excluded.connector_id,
					tunnel_id = excluded.tunnel_id,
					colo_name = excluded.colo_name,
					origin_ip = excluded.origin_ip,
					opened_at = excluded.opened_at,
					last_seen = excluded.last_seen`,
				c.UUID, c.ConnectorID, tunnelID, c.ColoName, c.OriginIP,
				toUnix(c.OpenedAt), toUnix(now),
			); err != nil {
				return fmt.Errorf("store: insert connection: %w", err)
			}
		}
		return nil
	})
}

// Connections returns the connections of a tunnel, or of every tunnel when
// tunnelID is empty.
func (s *Store) Connections(ctx context.Context, tunnelID string) ([]Connection, error) {
	query := `SELECT uuid, connector_id, tunnel_id, colo_name, origin_ip, opened_at, last_seen
		FROM connections`
	var args []any
	if tunnelID != "" {
		query += ` WHERE tunnel_id = ?`
		args = append(args, tunnelID)
	}
	query += ` ORDER BY tunnel_id, colo_name, uuid`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Connection
	for rows.Next() {
		var (
			c                  Connection
			openedAt, lastSeen int64
		)
		if err := rows.Scan(&c.UUID, &c.ConnectorID, &c.TunnelID, &c.ColoName, &c.OriginIP,
			&openedAt, &lastSeen); err != nil {
			return nil, err
		}
		c.OpenedAt = fromUnix(openedAt)
		c.LastSeen = fromUnix(lastSeen)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ReplaceIngress swaps the ingress rules of a tunnel and reports whether the
// rule set actually changed.
func (s *Store) ReplaceIngress(ctx context.Context, tunnelID string, rules []IngressRule, now time.Time) (bool, error) {
	previous, err := s.Ingress(ctx, tunnelID)
	if err != nil {
		return false, err
	}
	changed := ingressChanged(previous, rules)

	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM ingress WHERE tunnel_id = ?`, tunnelID); err != nil {
			return fmt.Errorf("store: clear ingress: %w", err)
		}
		for _, r := range rules {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO ingress (tunnel_id, idx, hostname, path, service, origin_request, config_version, seen_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				tunnelID, r.Index, r.Hostname, r.Path, r.Service, r.OriginRequest,
				r.ConfigVersion, toUnix(now),
			); err != nil {
				return fmt.Errorf("store: insert ingress: %w", err)
			}
		}
		return nil
	})

	return changed, err
}

func ingressChanged(previous, current []IngressRule) bool {
	if len(previous) != len(current) {
		return true
	}
	for i := range current {
		p, c := previous[i], current[i]
		if p.Hostname != c.Hostname || p.Path != c.Path || p.Service != c.Service ||
			p.OriginRequest != c.OriginRequest || p.ConfigVersion != c.ConfigVersion {
			return true
		}
	}
	return false
}

// Ingress returns the ingress rules of a tunnel, or of every tunnel when
// tunnelID is empty.
func (s *Store) Ingress(ctx context.Context, tunnelID string) ([]IngressRule, error) {
	query := `SELECT tunnel_id, idx, hostname, path, service, origin_request, config_version, seen_at
		FROM ingress`
	var args []any
	if tunnelID != "" {
		query += ` WHERE tunnel_id = ?`
		args = append(args, tunnelID)
	}
	query += ` ORDER BY tunnel_id, idx`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []IngressRule
	for rows.Next() {
		var (
			r      IngressRule
			seenAt int64
		)
		if err := rows.Scan(&r.TunnelID, &r.Index, &r.Hostname, &r.Path, &r.Service,
			&r.OriginRequest, &r.ConfigVersion, &seenAt); err != nil {
			return nil, err
		}
		r.SeenAt = fromUnix(seenAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReplaceAccessApps swaps the stored Access application inventory.
func (s *Store) ReplaceAccessApps(ctx context.Context, apps []AccessApp, now time.Time) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM access_apps`); err != nil {
			return fmt.Errorf("store: clear access apps: %w", err)
		}
		for _, a := range apps {
			domains, err := json.Marshal(a.Domains)
			if err != nil {
				return fmt.Errorf("store: encode domains: %w", err)
			}
			policies, err := json.Marshal(map[string]any{
				"hasBypass": a.HasBypass, "hasToken": a.HasToken, "count": a.PolicyCount,
			})
			if err != nil {
				return fmt.Errorf("store: encode policy summary: %w", err)
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO access_apps (id, name, domains, app_type, policies, last_seen)
				 VALUES (?, ?, ?, ?, ?, ?)`,
				a.ID, a.Name, string(domains), a.Type, string(policies), toUnix(now),
			); err != nil {
				return fmt.Errorf("store: insert access app: %w", err)
			}
		}
		return nil
	})
}

// AccessApps returns the stored Access application inventory.
func (s *Store) AccessApps(ctx context.Context) ([]AccessApp, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, domains, app_type, policies, last_seen FROM access_apps ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AccessApp
	for rows.Next() {
		var (
			a                 AccessApp
			domains, policies string
			lastSeen          int64
		)
		if err := rows.Scan(&a.ID, &a.Name, &domains, &a.Type, &policies, &lastSeen); err != nil {
			return nil, err
		}
		if domains != "" {
			_ = json.Unmarshal([]byte(domains), &a.Domains)
		}
		var summary struct {
			HasBypass bool `json:"hasBypass"`
			HasToken  bool `json:"hasToken"`
			Count     int  `json:"count"`
		}
		if policies != "" && json.Unmarshal([]byte(policies), &summary) == nil {
			a.HasBypass, a.HasToken, a.PolicyCount = summary.HasBypass, summary.HasToken, summary.Count
		}
		a.LastSeen = fromUnix(lastSeen)
		out = append(out, a)
	}
	return out, rows.Err()
}

// ReplaceServiceTokens swaps the stored Access service token metadata.
func (s *Store) ReplaceServiceTokens(ctx context.Context, tokens []ServiceToken, now time.Time) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM access_service_tokens`); err != nil {
			return fmt.Errorf("store: clear service tokens: %w", err)
		}
		for _, t := range tokens {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO access_service_tokens (id, name, client_id, expires_at, last_seen)
				 VALUES (?, ?, ?, ?, ?)`,
				t.ID, t.Name, t.ClientID, toUnix(t.ExpiresAt), toUnix(now),
			); err != nil {
				return fmt.Errorf("store: insert service token: %w", err)
			}
		}
		return nil
	})
}

// ServiceTokens returns the stored Access service token metadata.
func (s *Store) ServiceTokens(ctx context.Context) ([]ServiceToken, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, client_id, expires_at, last_seen FROM access_service_tokens ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ServiceToken
	for rows.Next() {
		var (
			t                   ServiceToken
			expiresAt, lastSeen int64
		)
		if err := rows.Scan(&t.ID, &t.Name, &t.ClientID, &expiresAt, &lastSeen); err != nil {
			return nil, err
		}
		t.ExpiresAt = fromUnix(expiresAt)
		t.LastSeen = fromUnix(lastSeen)
		out = append(out, t)
	}
	return out, rows.Err()
}

// AddEvents appends entries to the event log.
func (s *Store) AddEvents(ctx context.Context, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for _, e := range events {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO events (ts, kind, tunnel_id, hostname, from_state, to_state, message)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				toUnix(e.Timestamp), e.Kind, e.TunnelID, e.Hostname, e.FromState, e.ToState, e.Message,
			); err != nil {
				return fmt.Errorf("store: insert event: %w", err)
			}
		}
		return nil
	})
}

// Events returns the most recent log entries, newest first.
func (s *Store) Events(ctx context.Context, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, ts, kind, tunnel_id, hostname, from_state, to_state, message
		 FROM events ORDER BY ts DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var (
			e  Event
			ts int64
		)
		if err := rows.Scan(&e.ID, &ts, &e.Kind, &e.TunnelID, &e.Hostname,
			&e.FromState, &e.ToState, &e.Message); err != nil {
			return nil, err
		}
		e.Timestamp = fromUnix(ts)
		out = append(out, e)
	}
	return out, rows.Err()
}

// AddProbeResults appends probe outcomes.
func (s *Store) AddProbeResults(ctx context.Context, results []ProbeResult) error {
	if len(results) == 0 {
		return nil
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for _, r := range results {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO probe_results (hostname, checked_at, status_code, class, latency_ms, error)
				 VALUES (?, ?, ?, ?, ?, ?)`,
				r.Hostname, toUnix(r.CheckedAt), r.StatusCode, r.Class, r.LatencyMS, r.Error,
			); err != nil {
				return fmt.Errorf("store: insert probe result: %w", err)
			}
		}
		return nil
	})
}

// LatestProbeResults returns the most recent probe outcome per hostname.
func (s *Store) LatestProbeResults(ctx context.Context) (map[string]ProbeResult, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT p.hostname, p.checked_at, p.status_code, p.class, p.latency_ms, p.error
		 FROM probe_results p
		 JOIN (SELECT hostname, MAX(checked_at) AS checked_at FROM probe_results GROUP BY hostname) latest
		   ON latest.hostname = p.hostname AND latest.checked_at = p.checked_at
		 GROUP BY p.hostname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]ProbeResult{}
	for rows.Next() {
		var (
			r         ProbeResult
			checkedAt int64
		)
		if err := rows.Scan(&r.Hostname, &checkedAt, &r.StatusCode, &r.Class, &r.LatencyMS, &r.Error); err != nil {
			return nil, err
		}
		r.CheckedAt = fromUnix(checkedAt)
		out[r.Hostname] = r
	}
	return out, rows.Err()
}

// AddPollRun records the outcome of a collector cycle.
func (s *Store) AddPollRun(ctx context.Context, run PollRun) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO poll_runs (started_at, duration_ms, ok, error) VALUES (?, ?, ?, ?)`,
		toUnix(run.StartedAt), run.DurationMS, run.OK, run.Error)
	return err
}

// LastPollRun returns the most recent collector cycle, if any.
func (s *Store) LastPollRun(ctx context.Context) (PollRun, bool, error) {
	var (
		run       PollRun
		startedAt int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT started_at, duration_ms, ok, error FROM poll_runs ORDER BY started_at DESC, id DESC LIMIT 1`,
	).Scan(&startedAt, &run.DurationMS, &run.OK, &run.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return PollRun{}, false, nil
	}
	if err != nil {
		return PollRun{}, false, err
	}
	run.StartedAt = fromUnix(startedAt)
	return run, true, nil
}

// Prune deletes history, probe results, events and poll runs older than cutoff.
func (s *Store) Prune(ctx context.Context, cutoff time.Time) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for _, stmt := range []string{
			`DELETE FROM tunnel_status_history WHERE changed_at < ?`,
			`DELETE FROM probe_results WHERE checked_at < ?`,
			`DELETE FROM events WHERE ts < ?`,
			`DELETE FROM poll_runs WHERE started_at < ?`,
		} {
			if _, err := tx.ExecContext(ctx, stmt, toUnix(cutoff)); err != nil {
				return fmt.Errorf("store: prune: %w", err)
			}
		}
		return nil
	})
}
