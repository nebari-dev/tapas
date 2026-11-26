-- +goose Up
-- The authorization server a connector was pinned to when its MCP server's
-- metadata was first read. oauth_issuer is its issuer ('' while none is
-- pinned), oauth_server a JSON object of its endpoints and what it supports;
-- sign-ins, refreshes and revocations use only these, never a later
-- discovery. oauth_server_moved is set when the metadata later named another
-- issuer: no sign-in starts until an admin saves the connector again.
ALTER TABLE connectors ADD COLUMN oauth_issuer TEXT NOT NULL DEFAULT '';
ALTER TABLE connectors ADD COLUMN oauth_server TEXT NOT NULL DEFAULT '{}';
ALTER TABLE connectors ADD COLUMN oauth_server_moved BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE connectors DROP COLUMN oauth_server_moved;
ALTER TABLE connectors DROP COLUMN oauth_server;
ALTER TABLE connectors DROP COLUMN oauth_issuer;
