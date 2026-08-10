-- Aggregated Access authentication events per application. The raw log can hold
-- tens of thousands of entries a day, so only the summary is kept and the whole
-- table is replaced on every collection.
CREATE TABLE IF NOT EXISTS access_logins (
    app_domain TEXT PRIMARY KEY,
    allowed    INTEGER NOT NULL DEFAULT 0,
    denied     INTEGER NOT NULL DEFAULT 0,
    users      INTEGER NOT NULL DEFAULT 0,
    last_at    INTEGER NOT NULL DEFAULT 0
);
