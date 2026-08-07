-- Every notification the dispatcher decided to emit, delivered or not. It is
-- both the audit trail and what the cooldown is computed from, so a restart
-- cannot cause a repeat storm.
CREATE TABLE IF NOT EXISTS notifications (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    dedup_key  TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    severity   TEXT    NOT NULL DEFAULT '',
    kind       TEXT    NOT NULL DEFAULT '',
    tunnel_id  TEXT    NOT NULL DEFAULT '',
    hostname   TEXT    NOT NULL DEFAULT '',
    title      TEXT    NOT NULL DEFAULT '',
    body       TEXT    NOT NULL DEFAULT '',
    transport  TEXT    NOT NULL DEFAULT '',
    status     TEXT    NOT NULL DEFAULT '',
    error      TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_notifications_key_time
    ON notifications (dedup_key, created_at);
