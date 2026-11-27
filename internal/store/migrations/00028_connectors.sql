-- +goose Up
-- One row per connector an admin added: where its MCP server is, how people
-- sign in to it, who may use it and which of its tools change things.
-- oauth_client_secret_sealed is sealed by the gateway's sessions key; the
-- clear value is never stored. scopes and allowed_groups are JSON arrays of
-- strings, tool_modes a JSON object (tool name: read, write or off), tools a
-- JSON array snapshot of the upstream tool list. rev changes on every write.
CREATE TABLE connectors (
    id                         TEXT PRIMARY KEY,
    slug                       TEXT NOT NULL UNIQUE,
    name                       TEXT NOT NULL,
    catalog_id                 TEXT NOT NULL,
    url                        TEXT NOT NULL,
    auth_mode                  TEXT NOT NULL,
    enabled                    BOOLEAN NOT NULL,
    oauth_registration         TEXT NOT NULL,
    oauth_client_id            TEXT NOT NULL,
    oauth_client_secret_sealed BYTEA,
    oauth_authorize_url        TEXT NOT NULL,
    oauth_token_url            TEXT NOT NULL,
    oauth_revoke_url           TEXT NOT NULL,
    scopes                     TEXT NOT NULL,
    allowed_groups             TEXT NOT NULL,
    tool_modes                 TEXT NOT NULL,
    tools                      TEXT NOT NULL,
    rev                        TEXT NOT NULL,
    created_at                 BIGINT NOT NULL,
    updated_at                 BIGINT NOT NULL
);

-- +goose Down
DROP TABLE connectors;
