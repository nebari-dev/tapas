-- name: UpsertConnectorConnection :exec
INSERT INTO connector_connections (id, connector_id, subject, connected_by, resource, access_sealed, refresh_sealed,
    scopes, status, expires_at, refreshing_until, rev, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 0, $11, $12, $13)
ON CONFLICT (connector_id, subject) DO UPDATE SET id = excluded.id, connected_by = excluded.connected_by,
    resource = excluded.resource, access_sealed = excluded.access_sealed, refresh_sealed = excluded.refresh_sealed,
    scopes = excluded.scopes, status = excluded.status, expires_at = excluded.expires_at, refreshing_until = 0,
    rev = excluded.rev, created_at = excluded.created_at, updated_at = excluded.updated_at;

-- Takes the connector's row lock when it is still at rev, so a connection
-- is stored only for the revision it was obtained under, and an admin's
-- change waits for it (and then removes it) or makes it fail.
-- name: LockConnectorRev :execrows
UPDATE connectors SET rev = rev WHERE id = $1 AND rev = $2;

-- name: GetConnectorConnection :one
SELECT * FROM connector_connections WHERE connector_id = $1 AND subject = $2;

-- name: GetConnectorConnectionByID :one
SELECT * FROM connector_connections WHERE id = $1;

-- name: ListConnectorConnectionsForSubject :many
SELECT * FROM connector_connections WHERE subject = $1 ORDER BY connector_id;

-- name: ListConnectorConnectionsForConnector :many
SELECT * FROM connector_connections WHERE connector_id = $1 ORDER BY subject;

-- One caller wins the right to refresh until refreshing_until.
-- name: ClaimConnectorRefresh :execrows
UPDATE connector_connections SET refreshing_until = $1 WHERE id = $2 AND rev = $3 AND refreshing_until < $4;

-- Guarded by rev, so a refresh never overwrites a newer connection.
-- name: SetConnectorTokens :execrows
UPDATE connector_connections SET access_sealed = $1, refresh_sealed = $2, expires_at = $3, status = $4,
    refreshing_until = 0, rev = $5, updated_at = $6
WHERE id = $7 AND rev = $8;

-- name: SetConnectorConnectionStatus :execrows
UPDATE connector_connections SET status = $1, refreshing_until = 0, updated_at = $2 WHERE id = $3;

-- name: DeleteConnectorConnection :execrows
DELETE FROM connector_connections WHERE connector_id = $1 AND subject = $2;

-- name: DeleteConnectorConnectionsForConnector :execrows
DELETE FROM connector_connections WHERE connector_id = $1;

-- name: DeleteConnectorConnectionsForSubject :execrows
DELETE FROM connector_connections WHERE subject = $1 AND subject <> '';

-- name: InsertConnectorOAuthState :exec
INSERT INTO connector_oauth_states (state_hash, connector_id, connector_rev, person, subject, verifier_sealed,
    redirect_uri, issuer, created_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- Single use: the row is gone once read. The caller checks person and expiry.
-- name: TakeConnectorOAuthState :one
DELETE FROM connector_oauth_states WHERE state_hash = $1 RETURNING *;

-- name: SweepConnectorOAuthStates :execrows
DELETE FROM connector_oauth_states WHERE expires_at < $1;
