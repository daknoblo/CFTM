package store

import (
	"context"
	"strings"
	"time"
)

// FindingRef identifies one finding instance: the check that raised it plus the
// tunnel and hostname it applies to.
type FindingRef struct {
	Code     string `json:"code"`
	TunnelID string `json:"tunnelId,omitempty"`
	Hostname string `json:"hostname,omitempty"`
}

// Valid reports whether the reference carries at least a code.
func (r FindingRef) Valid() bool { return r.Code != "" }

// IgnoredFinding is a muted finding together with when it was muted.
type IgnoredFinding struct {
	FindingRef
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"createdAt"`
}

// IgnoreFinding mutes a finding. Muting the same one twice is not an error.
func (s *Store) IgnoreFinding(ctx context.Context, ref FindingRef, message string, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO ignored_findings (code, tunnel_id, hostname, note, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(code, tunnel_id, hostname) DO NOTHING`,
		ref.Code, ref.TunnelID, strings.ToLower(ref.Hostname), message, toUnix(now))
	return err
}

// RestoreFinding un-mutes a finding.
func (s *Store) RestoreFinding(ctx context.Context, ref FindingRef) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM ignored_findings WHERE code = ? AND tunnel_id = ? AND hostname = ?`,
		ref.Code, ref.TunnelID, strings.ToLower(ref.Hostname))
	return err
}

// IgnoredFindings returns every muted finding, newest first.
func (s *Store) IgnoredFindings(ctx context.Context) ([]IgnoredFinding, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT code, tunnel_id, hostname, note, created_at
		 FROM ignored_findings ORDER BY created_at DESC, code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []IgnoredFinding
	for rows.Next() {
		var (
			f         IgnoredFinding
			createdAt int64
		)
		if err := rows.Scan(&f.Code, &f.TunnelID, &f.Hostname, &f.Message, &createdAt); err != nil {
			return nil, err
		}
		f.CreatedAt = fromUnix(createdAt)
		out = append(out, f)
	}
	return out, rows.Err()
}

// IgnoredFindingSet returns the muted findings as a lookup set.
func (s *Store) IgnoredFindingSet(ctx context.Context) (map[FindingRef]bool, error) {
	ignored, err := s.IgnoredFindings(ctx)
	if err != nil {
		return nil, err
	}
	set := make(map[FindingRef]bool, len(ignored))
	for _, f := range ignored {
		set[f.FindingRef] = true
	}
	return set, nil
}
