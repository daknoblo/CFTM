package store

import (
	"context"
	"time"
)

// Notification delivery outcomes.
const (
	NotificationSent   = "sent"
	NotificationFailed = "failed"
)

// Notification is one entry of the outbox.
type Notification struct {
	ID        int64     `json:"id"`
	Key       string    `json:"key"`
	CreatedAt time.Time `json:"createdAt"`
	Severity  string    `json:"severity"`
	Kind      string    `json:"kind"`
	TunnelID  string    `json:"tunnelId,omitempty"`
	Hostname  string    `json:"hostname,omitempty"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Transport string    `json:"transport"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
}

// AddNotification records one notification attempt.
func (s *Store) AddNotification(ctx context.Context, n Notification) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO notifications
		   (dedup_key, created_at, severity, kind, tunnel_id, hostname, title, body, transport, status, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.Key, toUnix(n.CreatedAt), n.Severity, n.Kind, n.TunnelID, n.Hostname,
		n.Title, n.Body, n.Transport, n.Status, n.Error)
	return err
}

// NotifiedSince reports whether the same condition was already announced at or
// after since, which is what keeps a flapping tunnel from spamming.
func (s *Store) NotifiedSince(ctx context.Context, key string, since time.Time) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM notifications WHERE dedup_key = ? AND created_at >= ?`,
		key, toUnix(since)).Scan(&count)
	return count > 0, err
}

// Notifications returns the outbox, newest first.
func (s *Store) Notifications(ctx context.Context, limit int) ([]Notification, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, dedup_key, created_at, severity, kind, tunnel_id, hostname,
		        title, body, transport, status, error
		 FROM notifications ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Notification
	for rows.Next() {
		var (
			n         Notification
			createdAt int64
		)
		if err := rows.Scan(&n.ID, &n.Key, &createdAt, &n.Severity, &n.Kind,
			&n.TunnelID, &n.Hostname, &n.Title, &n.Body, &n.Transport,
			&n.Status, &n.Error); err != nil {
			return nil, err
		}
		n.CreatedAt = fromUnix(createdAt)
		out = append(out, n)
	}
	return out, rows.Err()
}
