CREATE TABLE IF NOT EXISTS tunnels (
    id                TEXT PRIMARY KEY,
    name              TEXT    NOT NULL DEFAULT '',
    tun_type          TEXT    NOT NULL DEFAULT '',
    config_src        TEXT    NOT NULL DEFAULT '',
    remote_config     INTEGER NOT NULL DEFAULT 0,
    status            TEXT    NOT NULL DEFAULT '',
    created_at        INTEGER NOT NULL DEFAULT 0,
    conns_active_at   INTEGER NOT NULL DEFAULT 0,
    conns_inactive_at INTEGER NOT NULL DEFAULT 0,
    first_seen        INTEGER NOT NULL DEFAULT 0,
    last_seen         INTEGER NOT NULL DEFAULT 0
);

-- One row per observed status transition, never one row per poll.
CREATE TABLE IF NOT EXISTS tunnel_status_history (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    tunnel_id   TEXT    NOT NULL,
    from_status TEXT    NOT NULL DEFAULT '',
    status      TEXT    NOT NULL,
    changed_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_status_history_tunnel_time
    ON tunnel_status_history (tunnel_id, changed_at);

CREATE TABLE IF NOT EXISTS connectors (
    id             TEXT PRIMARY KEY,
    tunnel_id      TEXT    NOT NULL,
    version        TEXT    NOT NULL DEFAULT '',
    arch           TEXT    NOT NULL DEFAULT '',
    config_version INTEGER NOT NULL DEFAULT 0,
    features       TEXT    NOT NULL DEFAULT '',
    run_at         INTEGER NOT NULL DEFAULT 0,
    last_seen      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_connectors_tunnel ON connectors (tunnel_id);

CREATE TABLE IF NOT EXISTS connections (
    uuid         TEXT PRIMARY KEY,
    connector_id TEXT    NOT NULL DEFAULT '',
    tunnel_id    TEXT    NOT NULL,
    colo_name    TEXT    NOT NULL DEFAULT '',
    origin_ip    TEXT    NOT NULL DEFAULT '',
    opened_at    INTEGER NOT NULL DEFAULT 0,
    last_seen    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_connections_tunnel ON connections (tunnel_id);

CREATE TABLE IF NOT EXISTS ingress (
    tunnel_id      TEXT    NOT NULL,
    idx            INTEGER NOT NULL,
    hostname       TEXT    NOT NULL DEFAULT '',
    path           TEXT    NOT NULL DEFAULT '',
    service        TEXT    NOT NULL DEFAULT '',
    origin_request TEXT    NOT NULL DEFAULT '',
    config_version INTEGER NOT NULL DEFAULT 0,
    seen_at        INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (tunnel_id, idx)
);

CREATE TABLE IF NOT EXISTS access_apps (
    id        TEXT PRIMARY KEY,
    name      TEXT    NOT NULL DEFAULT '',
    domains   TEXT    NOT NULL DEFAULT '',
    app_type  TEXT    NOT NULL DEFAULT '',
    policies  TEXT    NOT NULL DEFAULT '',
    last_seen INTEGER NOT NULL DEFAULT 0
);

-- Only token metadata; the client secret is never retrievable from the API.
CREATE TABLE IF NOT EXISTS access_service_tokens (
    id         TEXT PRIMARY KEY,
    name       TEXT    NOT NULL DEFAULT '',
    client_id  TEXT    NOT NULL DEFAULT '',
    expires_at INTEGER NOT NULL DEFAULT 0,
    last_seen  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS probe_results (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    hostname    TEXT    NOT NULL,
    checked_at  INTEGER NOT NULL,
    status_code INTEGER NOT NULL DEFAULT 0,
    class       TEXT    NOT NULL DEFAULT '',
    latency_ms  INTEGER NOT NULL DEFAULT 0,
    error       TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_probe_hostname_time
    ON probe_results (hostname, checked_at);

CREATE TABLE IF NOT EXISTS events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    ts         INTEGER NOT NULL,
    kind       TEXT    NOT NULL,
    tunnel_id  TEXT    NOT NULL DEFAULT '',
    hostname   TEXT    NOT NULL DEFAULT '',
    from_state TEXT    NOT NULL DEFAULT '',
    to_state   TEXT    NOT NULL DEFAULT '',
    message    TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_events_ts ON events (ts);

CREATE TABLE IF NOT EXISTS poll_runs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at  INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    ok          INTEGER NOT NULL DEFAULT 0,
    error       TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_poll_runs_started ON poll_runs (started_at);

CREATE TABLE IF NOT EXISTS meta (
    key        TEXT PRIMARY KEY,
    value      TEXT    NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL DEFAULT 0
);
