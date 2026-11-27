-- +goose Up
-- Every connection is one person's own: a connector no longer has one
-- account an admin connects for everyone. That account's connections
-- (subject '') and the sign-ins started for them go, so their tokens do not
-- linger. A connector that was in that mode is turned off (with a new rev)
-- for an admin to review before people connect their own accounts to it.
DELETE FROM connector_oauth_states WHERE subject <> person;
DELETE FROM connector_connections WHERE subject = '';
UPDATE connectors SET enabled = FALSE, rev = rev || '-31' WHERE auth_mode = 'shared';
ALTER TABLE connectors DROP COLUMN auth_mode;
ALTER TABLE connector_oauth_states DROP COLUMN subject;

-- +goose Down
ALTER TABLE connector_oauth_states ADD COLUMN subject TEXT NOT NULL DEFAULT '';
UPDATE connector_oauth_states SET subject = person;
ALTER TABLE connectors ADD COLUMN auth_mode TEXT NOT NULL DEFAULT 'user';
