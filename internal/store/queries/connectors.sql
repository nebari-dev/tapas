-- A taken slug inserts nothing.
-- name: InsertConnector :execrows
INSERT INTO connectors (id, slug, name, catalog_id, url, auth_mode, enabled, oauth_registration, oauth_client_id,
    oauth_client_secret_sealed, oauth_authorize_url, oauth_token_url, oauth_revoke_url, scopes, allowed_groups,
    tool_modes, tools, rev, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
ON CONFLICT (slug) DO NOTHING;

-- name: GetConnector :one
SELECT * FROM connectors WHERE id = $1;

-- name: GetConnectorBySlug :one
SELECT * FROM connectors WHERE slug = $1;

-- name: ListConnectors :many
SELECT * FROM connectors ORDER BY slug;

-- name: ListEnabledConnectors :many
SELECT * FROM connectors WHERE enabled ORDER BY slug;

-- Guarded by rev: no row means someone else saved first.
-- name: UpdateConnector :execrows
UPDATE connectors SET name = $1, url = $2, auth_mode = $3, enabled = $4, oauth_registration = $5, oauth_client_id = $6,
    oauth_client_secret_sealed = $7, oauth_authorize_url = $8, oauth_token_url = $9, oauth_revoke_url = $10,
    scopes = $11, allowed_groups = $12, tool_modes = $13, oauth_issuer = $14, oauth_server = $15,
    oauth_server_moved = $16, rev = $17, updated_at = $18
WHERE id = $19 AND rev = $20;

-- Pins the authorization server once: guarded by rev and by none pinned yet.
-- name: PinConnectorAuthServer :execrows
UPDATE connectors SET oauth_issuer = $1, oauth_server = $2, oauth_server_moved = FALSE, rev = $3, updated_at = $4
WHERE id = $5 AND rev = $6 AND oauth_issuer = '';

-- name: MarkConnectorAuthServerMoved :execrows
UPDATE connectors SET oauth_server_moved = TRUE, rev = $1, updated_at = $2
WHERE id = $3 AND oauth_issuer = $4 AND NOT oauth_server_moved;

-- name: SetConnectorTools :execrows
UPDATE connectors SET tools = $1, rev = $2, updated_at = $3 WHERE id = $4;

-- name: DeleteConnector :execrows
DELETE FROM connectors WHERE id = $1;
