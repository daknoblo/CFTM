-- Where requests to a hostname came from, aggregated per country. Only the
-- country is kept: the client IP is personal data and answers no question the
-- country does not. The table is replaced on every collection, like the other
-- summaries.
--
-- source records which API the row came from, because they see different
-- traffic: an Access bypass policy is invisible to the login datasets and only
-- shows up in the zone-scoped HTTP analytics.
CREATE TABLE IF NOT EXISTS request_origins (
    source       TEXT    NOT NULL,
    hostname     TEXT    NOT NULL DEFAULT '',
    path         TEXT    NOT NULL DEFAULT '',
    country      TEXT    NOT NULL DEFAULT '',
    allowed      INTEGER NOT NULL DEFAULT 0,
    denied       INTEGER NOT NULL DEFAULT 0,
    requests     INTEGER NOT NULL DEFAULT 0,
    -- sampled marks estimates, which the adaptive analytics datasets return
    -- once the request volume grows.
    sampled      INTEGER NOT NULL DEFAULT 0,
    window_start INTEGER NOT NULL DEFAULT 0,
    last_at      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (source, hostname, path, country)
);

CREATE INDEX IF NOT EXISTS idx_request_origins_hostname
    ON request_origins (hostname);
CREATE INDEX IF NOT EXISTS idx_request_origins_country
    ON request_origins (country);
