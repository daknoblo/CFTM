-- Findings the operator has acknowledged and no longer wants to see. A finding
-- is identified by its code plus the tunnel and hostname it was raised for, so
-- muting one public hostname leaves the same check active everywhere else.
CREATE TABLE IF NOT EXISTS ignored_findings (
    code       TEXT    NOT NULL,
    tunnel_id  TEXT    NOT NULL DEFAULT '',
    hostname   TEXT    NOT NULL DEFAULT '',
    note       TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (code, tunnel_id, hostname)
);
