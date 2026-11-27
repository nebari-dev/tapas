-- +goose Up
-- The token vault: one row per person and connector (subject is the person's
-- name), or one row with subject '' for a company-wide connector.
-- access_sealed and refresh_sealed are sealed by the gateway's sessions key
-- and bound to this row. resource is the MCP URL the tokens were issued for;
-- they are sent nowhere else. expires_at is NULL when the vendor gave none.
-- refreshing_until is a short claim so two replicas do not refresh at once.
CREATE TABLE connector_connections (
    id               TEXT PRIMARY KEY,
    connector_id     TEXT NOT NULL REFERENCES connectors (id) ON DELETE CASCADE,
    subject          TEXT NOT NULL,
    connected_by     TEXT NOT NULL,
    resource         TEXT NOT NULL,
    access_sealed    BYTEA NOT NULL,
    refresh_sealed   BYTEA,
    scopes           TEXT NOT NULL,
    status           TEXT NOT NULL,
    expires_at       BIGINT,
    refreshing_until BIGINT NOT NULL,
    rev              TEXT NOT NULL,
    created_at       BIGINT NOT NULL,
    updated_at       BIGINT NOT NULL,
    UNIQUE (connector_id, subject)
);
CREATE INDEX connector_connections_subject ON connector_connections (subject);

-- An OAuth sign-in in progress. state_hash is the SHA-256 of the state value
-- only the browser holds. person started it and is the only one who may
-- finish it. verifier_sealed is the PKCE verifier. Deleted when used.
CREATE TABLE connector_oauth_states (
    state_hash      BYTEA PRIMARY KEY,
    connector_id    TEXT NOT NULL REFERENCES connectors (id) ON DELETE CASCADE,
    connector_rev   TEXT NOT NULL,
    person          TEXT NOT NULL,
    subject         TEXT NOT NULL,
    verifier_sealed BYTEA NOT NULL,
    redirect_uri    TEXT NOT NULL,
    issuer          TEXT NOT NULL,
    created_at      BIGINT NOT NULL,
    expires_at      BIGINT NOT NULL
);
CREATE INDEX connector_oauth_states_expires_at ON connector_oauth_states (expires_at);

-- +goose Down
DROP INDEX connector_oauth_states_expires_at;
DROP TABLE connector_oauth_states;
DROP INDEX connector_connections_subject;
DROP TABLE connector_connections;
