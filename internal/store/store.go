// Package store persists tunnel snapshots, status history and events in SQLite.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, keeps CGO_ENABLED=0
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store is the SQLite-backed persistence layer.
type Store struct {
	db *sql.DB
}

// Open creates or opens the database at path and applies pending migrations.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("store: create data dir: %w", err)
		}
	}

	dsn := "file:" + url.PathEscape(path) +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(1)" +
		// Keeps sorts and temp tables off /tmp, which a read-only container
		// filesystem does not provide.
		"&_pragma=temp_store(MEMORY)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// SQLite serializes writers anyway; a single connection avoids SQLITE_BUSY.
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		name       TEXT PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	entries, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("store: list migrations: %w", err)
	}
	sort.Strings(entries)

	for _, name := range entries {
		var applied int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, name,
		).Scan(&applied); err != nil {
			return fmt.Errorf("store: check migration %s: %w", name, err)
		}
		if applied > 0 {
			continue
		}

		body, err := migrationsFS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("store: read migration %s: %w", name, err)
		}
		if _, err := s.db.Exec(string(body)); err != nil {
			return fmt.Errorf("store: apply migration %s: %w", name, err)
		}
		if _, err := s.db.Exec(
			`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`,
			name, time.Now().Unix(),
		); err != nil {
			return fmt.Errorf("store: record migration %s: %w", name, err)
		}
	}
	return nil
}

// withTx runs fn inside a transaction, rolling back on error.
func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// toUnix encodes a time for storage; the zero time becomes 0.
func toUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// fromUnix decodes a stored time; 0 becomes the zero time.
func fromUnix(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}

// SetMeta stores a small key/value pair, used for caches such as the latest
// known cloudflared release.
func (s *Store) SetMeta(ctx context.Context, key, value string, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO meta (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, toUnix(now))
	return err
}

// GetMeta returns a stored value and when it was written. A missing key yields
// an empty value and the zero time without an error.
func (s *Store) GetMeta(ctx context.Context, key string) (string, time.Time, error) {
	var (
		value     string
		updatedAt int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT value, updated_at FROM meta WHERE key = ?`, key,
	).Scan(&value, &updatedAt)
	if err == sql.ErrNoRows {
		return "", time.Time{}, nil
	}
	if err != nil {
		return "", time.Time{}, err
	}
	return value, fromUnix(updatedAt), nil
}
