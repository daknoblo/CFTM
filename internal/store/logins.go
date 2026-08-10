package store

import (
	"context"
	"database/sql"
	"time"
)

// AccessLogin is the authentication summary of one Access application.
type AccessLogin struct {
	AppDomain string    `json:"appDomain"`
	Allowed   int       `json:"allowed"`
	Denied    int       `json:"denied"`
	Users     int       `json:"users"`
	LastAt    time.Time `json:"lastAt"`
}

// ReplaceAccessLogins swaps the whole summary for a freshly collected one.
func (s *Store) ReplaceAccessLogins(ctx context.Context, logins []AccessLogin) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM access_logins`); err != nil {
			return err
		}
		for _, l := range logins {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO access_logins (app_domain, allowed, denied, users, last_at)
				 VALUES (?, ?, ?, ?, ?)`,
				l.AppDomain, l.Allowed, l.Denied, l.Users, toUnix(l.LastAt)); err != nil {
				return err
			}
		}
		return nil
	})
}

// AccessLogins returns the summary, busiest application first.
func (s *Store) AccessLogins(ctx context.Context) ([]AccessLogin, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT app_domain, allowed, denied, users, last_at
		 FROM access_logins ORDER BY allowed + denied DESC, app_domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AccessLogin
	for rows.Next() {
		var (
			l      AccessLogin
			lastAt int64
		)
		if err := rows.Scan(&l.AppDomain, &l.Allowed, &l.Denied, &l.Users, &lastAt); err != nil {
			return nil, err
		}
		l.LastAt = fromUnix(lastAt)
		out = append(out, l)
	}
	return out, rows.Err()
}
