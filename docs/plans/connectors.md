# Connectors: registry, connect-your-account, broker

Implementation plan for issues #2 (broker), #4 (writes through propose and approve), #5 (connectors) and the hook for #3 (policy), under epic #10.
The module path and many identifiers still say `javo`; paths below are as they are in the repo today.

## 1. Goals and non-goals

Goals

- An admin turns on a connector (Slack, GitHub, Notion, HubSpot, Google Gmail/Calendar/Drive, or any MCP URL) by saving one database row. No code change per connector.
- A person connects their own vendor account once, in the browser. Tapas keeps the tokens encrypted and refreshes them.
- Agents reach every connector through Tapas's one MCP endpoint (`/mcp`). They see only the tools their owner may use, and never a vendor token.
- Every connector call passes, in this order: identity from the Tapas token, authz, policy hook, approval for writes, vault, forward, audit.
- Writes become pending plans. The exact stored call runs once, after the owner approves in the browser.

Non-goals (this plan)

- Writing the connector MCP servers themselves (#5). The broker only needs an MCP URL.
- OPA itself (#3). Only the `policy.Checker` interface and an allow-all default ship here.
- NetworkPolicy that makes in-cluster connectors accept traffic only from the gateway (#2, depends on nebari-infrastructure-core#675).
- Returning a write's result content to the agent. After approval the agent sees `applied` or `failed` through the existing `get_plan` tool, nothing more.
- MCP resources, prompts, sampling and elicitation from connectors. Tools only.
- Rotating the sessions key. Connector tokens are sealed with it (see D3), and no rotation is added.
- `javo local`. That build carries no MCP endpoint today (`internal/gateway/mcp_local.go`) and carries no connectors either.

## 2. What exists today (facts this plan builds on)

- `internal/mcp/server.go`: one `sdk.Server` with a fixed tool set (read tools generated from the OpenAPI document, one propose tool per catalog action). Tool calls are in-process requests to the `/api` mux with the caller's own `Authorization` header (`Config.call`). Mounted by `internal/gateway/mcp.go` (build tag `!local`).
- The MCP bearer check (`tokenVerifier`) returns only `auth.TokenInfo{UserID, Scopes}`. The owner's groups are resolved in `authn.Authenticator.liveToken`, which `LiveToken` throws away.
- `internal/auth/token.Token` names the owner (`Owner`), the token (`ID`, `Name`), and the app or job it was made for (`App`, `Job`). `authn.AgentCaps` says whether that app is a coding agent.
- `internal/authz.Decide(p, roles, act, obj)` is the only place access is decided. It denies by default.
- `internal/plans.Service.Approve` claims a plan (`pending` to `applying`) in one conditional update, runs `apply` once, then records `applied` or `failed` with an audit row. A second approval gets `ErrDone`, an expired plan gets `ErrExpired`. Approve and reject routes are browser-session only (`authn.NoTokens`, `IsPlanDecision`).
- `actions.Action` is `Info/Plan/Apply`; `actions.Proposal` is `{Action, Target, Owner, Params map[string]string}`. The approve handler in `internal/gateway/apps/plans.go` looks the action up in the catalog and passes its `Apply`.
- Store: goose migrations in `internal/store/migrations` (one SQL set for both engines, next free number is 00028), sqlc queries in `internal/store/queries` generated with the PostgreSQL engine and required to run on SQLite too (no `::` casts, no JSON operators, placeholders ascending). Writes with an audit row go through `Store.mutate`; `Audit().Append` is for rows not tied to a store write.
- Sealing: people's secrets use an HPKE keyset whose private half only the controller holds, so the gateway cannot open them. Values the gateway itself must read back (the admin's AI key, webhook secrets, session provider tokens) are sealed with the gateway's sessions key, a `tink.AEAD` shared by all replicas, with associated data that binds the ciphertext to its row and use.
- The session cookie is `SameSite=Strict`, so a redirect from a vendor's site back to Tapas arrives without it.
- `go.mod` already has `github.com/modelcontextprotocol/go-sdk v1.8.0` (packages `mcp`, `auth`, `oauthex`) and `golang.org/x/oauth2 v0.37.0`.
- `test/localbuild` fails if a `local` build links `github.com/modelcontextprotocol/go-sdk` or `internal/mcp`.

## 3. Decisions

D1. Package layout. Small packages with one job each, so slices touch different folders.

| Package | Job | Imports go-sdk |
|---|---|---|
| `internal/connectors` | Types (`Connector`, `AuthMode`, `ToolMode`, `Caller`), slug and URL validation, tool-name split and join | no |
| `internal/connectors/catalog` | Built-in catalogue: `catalog.json` embedded with `go:embed`, loader, validation | no |
| `internal/connectors/registry` | CRUD over `store.Connectors`, seals the OAuth client secret, per-`rev` cache | no |
| `internal/connectors/vault` | Seal, open, refresh and delete tokens over `store.ConnectorConnections` | no |
| `internal/connectors/oauth` | Discovery, dynamic registration, authorize URL, code exchange, refresh, revoke | yes (`auth`, `oauthex`) |
| `internal/connectors/client` | Generic MCP client: list tools, call a tool, bearer passed per request | yes (`mcp`) |
| `internal/connectors/policy` | `Checker` interface, `AllowAll` | no |
| `internal/connectors/broker` | The pipeline: authz, policy, plan or forward, audit | yes |
| `internal/connectors/connectorstest` | Fakes: OAuth authorization server, MCP connector | yes |
| `internal/store` (`connectors.go`, `connector_connections.go`) | Rows and queries | no |
| `internal/gateway/connectors` | huma routes (admin, member, OAuth complete) | yes, so registered only from a `!local` file |

D2. Registry row is the whole configuration. A catalogue entry is only a prefilled form: enabling it copies its fields into a `connectors` row, and from then on the row alone is read. A custom connector is the same row with `catalog_id = ''`. Reason: one code path, and a catalogue change in a later release never silently changes a live connector.

D3. Vault key. Tokens and the OAuth client secret are sealed with the gateway's sessions key (`tink.AEAD`), not the HPKE secrets keyset. Reason: the broker runs in the gateway and must open tokens on every call; the HPKE private half is held by the controller only. This is the same choice `internal/ai/aisettings` made for the admin's AI key. No new key and no key derivation: Tink AEAD with associated data `"javo connector v1\x00" + table + "\x00" + row id + "\x00" + column`, so a ciphertext copied to another row or column does not open. Consequence: connectors need a sessions key that survives restarts. The gateway refuses to enable connectors when `--sessions-key-secret` is empty (the key is then made at start and every stored token would be lost on restart).

D4. (Replaced later: the `shared` mode is removed and every person connects their own account; see Status.) Auth modes: `user` (each person connects their own account; the connection row's `subject` is the person) and `shared` (company-wide; an admin connects once; `subject` is `''`). In `shared` mode every allowed person's call uses the one shared token. The audit row still names the real person and agent, and the UI tells the admin to connect a service account, because everything done through it appears at the vendor as that account.

D5. OAuth endpoints: discovered by default (RFC 9728 from the MCP URL, then RFC 8414), with optional static `authorize/token/revoke` URLs on the row. Reason: a vendor MCP server that publishes no metadata, and an in-cluster MCP server in front of a vendor API (decision 4 of the architecture), both still work as a config row. The `resource` parameter (RFC 8707) is sent when endpoints were discovered, and left out with static endpoints.

D6. Client registration: use the pasted client id and secret when present; otherwise, when the authorization server advertises `registration_endpoint`, register dynamically (RFC 7591) once and store the result on the row with `oauth_registration = 'dynamic'`. If neither is possible the admin sees "paste an OAuth client".

D7. OAuth callback lands on a UI page, not an API route. Each connector has its own redirect URI, `{publicURL}/settings/connections/callback/{slug}` (a page of the SPA, served without a session; the slug is known before the connector exists, so the admin form shows it while it is typed). The page then calls `POST /api/connectors/oauth/complete` with `{slug, state, code, iss}` from the same origin; the server refuses a state started for another connector's redirect URI, and for discovered endpoints an iss other than the pinned issuer (the OAuth security BCP's mix-up defences), which carries the `SameSite=Strict` session cookie and passes `authn.SameOrigin`. Reason: the cookie is not sent on the vendor's redirect, and this binds completion to the signed-in person with the checks every other write already has. The code in the page URL is harmless alone: it is single use and useless without the PKCE verifier, which never leaves the server. The page replaces its history entry before calling.

D8. Pending OAuth state lives in the database (`connector_oauth_states`), not in a cookie or memory. Reason: any replica can complete it, and it can be consumed exactly once with `DELETE ... RETURNING`. The row is keyed by the SHA-256 of the state (the state itself is never stored, as with plan ids and device codes), holds the sealed PKCE verifier, the person who started it, the connector's `rev` and the redirect URI used, and expires after 10 minutes. A janitor sweeps expired rows.

D9. Tool names on the wire are `<slug>__<tool>`, for example `slack__post_message`; the UI and the audit log show `slack.post_message`. Slugs match `^[a-z][a-z0-9]{1,30}$`. Reason for `__` and not `.`: the go-sdk accepts dots, but model APIs commonly restrict tool names to letters, digits, `_` and `-`. Confirm against the MCP clients we target at the start of slice 2 and switch the one constant if dots are safe everywhere. Tapas's own tools never contain `__` (a test asserts it), so the first `__` splits unambiguously. Names longer than 128 characters after prefixing are dropped from the list and logged.

D10. Per-caller `tools/list` with one server. Keep the single `sdk.Server` and add one receiving middleware (`Server.AddReceivingMiddleware`) in `internal/mcp`: on `tools/list` it appends the broker's tools for the caller; on `tools/call` with a `__` name it routes to the broker and never reaches the static handlers. Reason: no per-user servers, no change to sessions or the session cap.

D11. Upstream `tools/list` cache: in memory, per `(connector id, connector rev, subject)`, 5 minute TTL, at most 1024 entries (oldest dropped). A connector that fails to list is left out of that answer and logged; it never fails the whole `tools/list`. A person with no connection to a `user` connector sees none of its tools; the generated `list_connectors` tool (from `GET /api/connectors`) tells the agent which connectors exist and where the person connects them.

D12. How write tools are flagged: `connectors.tool_modes` is a JSON object `{"tool": "read" | "write" | "off"}` set by the admin. A tool not in the map is `write`. Upstream `readOnlyHint` only prefills the admin form; it is never trusted at call time, because the connector could change it. `off` tools are not listed and not callable. The admin form is filled from `connectors.tools`, a snapshot of the upstream list (name, description, `readOnlyHint`) refreshed on demand.

D13. Upstream sessions: one MCP client session per call (initialize, call, close), `DisableStandaloneSSE: true`. Reason: nothing to share between replicas, no stale session after a token refresh, and the bearer is naturally per request. Pooling is a later optimisation if latency needs it.

D14. Timeouts and limits: connect plus initialize 10 s; `tools/list` 15 s; `tools/call` 60 s; OAuth HTTP calls 15 s; revoke 5 s; state TTL 10 min; upstream response body cap 4 MiB at the HTTP layer and tool result cap 1 MiB (the existing `maxResult`); arguments cap 64 KiB. No redirects are followed on MCP, token, registration or revocation requests.

D15. Who may call connector tools: a personal token, or the token of a coding-agent app (the app has `AgentCaps`, which already requires the app to be private). Any other app or job token is refused, for the reason `authn.NoAppTokens` gives for secrets: people the app is shared with could make it act. Reads need any valid scope; writes need a scope that may `POST /api/plans`.

D16. Authz: a new `ActUseConnector`. Allowed when the person is signed in, the connector is enabled, and one of their groups is in `allowed_groups` (the entry `*` means every member). An empty list allows nobody. A platform admin gets no implicit use, as with secrets: admins configure connectors, groups grant use. Denial answers "not found" so it does not reveal that a connector exists.

D17. Policy hook: `policy.Checker.Check(ctx, Input) (Decision, error)` with effects `allow`, `deny`, `require_approval`. An error or an unknown effect is a deny. It runs when a call is proposed or read, and again when an approved write is applied.

D18. Writes reuse plans unchanged. One hidden catalog action `connector-call` whose `Plan` does the checks and whose `Apply` forwards the stored call. The broker proposes through `POST /api/plans` in process with the caller's `Authorization` header, exactly as today's propose tools do, so scope checks, the pending-plan cap and `plans.Requester` come for free. "Once" is the existing `pending` to `applying` claim; a crash while applying leaves the plan `applying` and it is never retried.

D19. Audit: one `audit_events` row per brokered call, appended after the outcome is known. If the row cannot be written, the result is withheld and the call answers an error, so no unaudited data leaves. Rows hold names and outcomes only.

D20. Changing a connector's URL (any change, compared exactly), OAuth endpoints or client deletes its connections (people reconnect) and requires the client secret again; so does saving it after its authorization server moved (below). Changing its auth mode deletes its connections too. The authorization server is pinned (issuer and endpoints, on the connector row) at the first discovery; finishing a sign-in, refreshing and revoking use only the pinned values, and when the MCP server's metadata later names another issuer no sign-in starts until an admin saves the connector again. Reason: a stored token, a code or the client secret must never be sent to a host other than the one it was issued for, and a later metadata change must not redirect them.

D21. No new Go or npm dependencies. `golang.org/x/oauth2` (PKCE helpers, code exchange, refresh) and the go-sdk `oauthex` and `auth` discovery helpers are already in `go.mod`. The go-sdk's `auth.AuthorizationCodeHandler` is not used: it blocks in one process waiting for the code, which does not fit a browser redirect served by several replicas.

## 4. Data model

Two migrations, numbers fixed here so parallel work does not collide: `00028` belongs to slice 1, `00029` to slice 3. Times are Unix milliseconds as elsewhere. JSON is stored as `TEXT` and decoded in Go (no JSON operators).

`internal/store/migrations/00028_connectors.sql`

```sql
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
```

`auth_mode` is `user` or `shared`; `oauth_registration` is `static` (pasted) or `dynamic` (RFC 7591).

`internal/store/migrations/00029_connector_connections.sql`

```sql
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
```

`status` is `active` or `needs_reauth`. `sqlc.yaml` gets overrides for `connector_connections.expires_at` (`dbtime.NullTime`), `connector_connections.refreshing_until` and `connector_oauth_states.expires_at` (`dbtime.Time`); `created_at` and `updated_at` are covered by the existing wildcards.

`internal/store/queries/connectors.sql` (slice 1)

```sql
-- name: InsertConnector :exec
INSERT INTO connectors (id, slug, name, catalog_id, url, auth_mode, enabled, oauth_registration, oauth_client_id,
    oauth_client_secret_sealed, oauth_authorize_url, oauth_token_url, oauth_revoke_url, scopes, allowed_groups,
    tool_modes, tools, rev, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20);

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
    scopes = $11, allowed_groups = $12, tool_modes = $13, rev = $14, updated_at = $15
WHERE id = $16 AND rev = $17;

-- name: SetConnectorTools :execrows
UPDATE connectors SET tools = $1, rev = $2, updated_at = $3 WHERE id = $4;

-- name: DeleteConnector :execrows
DELETE FROM connectors WHERE id = $1;
```

`internal/store/queries/connector_connections.sql` (slice 3)

```sql
-- name: UpsertConnectorConnection :exec
INSERT INTO connector_connections (id, connector_id, subject, connected_by, resource, access_sealed, refresh_sealed,
    scopes, status, expires_at, refreshing_until, rev, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 0, $11, $12, $13)
ON CONFLICT (connector_id, subject) DO UPDATE SET id = excluded.id, connected_by = excluded.connected_by,
    resource = excluded.resource, access_sealed = excluded.access_sealed, refresh_sealed = excluded.refresh_sealed,
    scopes = excluded.scopes, status = excluded.status, expires_at = excluded.expires_at, refreshing_until = 0,
    rev = excluded.rev, updated_at = excluded.updated_at;

-- name: GetConnectorConnection :one
SELECT * FROM connector_connections WHERE connector_id = $1 AND subject = $2;

-- name: ListConnectorConnectionsForSubject :many
SELECT * FROM connector_connections WHERE subject = $1;

-- name: ListConnectorConnectionsForConnector :many
SELECT * FROM connector_connections WHERE connector_id = $1;

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
```

`DELETE ... RETURNING *` already runs on both engines (`queries/sessions.sql`). The sealed id in the upsert: the ciphertexts are bound to the new row id, so the upsert replaces `id` together with the ciphertexts.

Store API (Go): `Store.Connectors()` with `Create/Get/BySlug/List/ListEnabled/Update/SetTools/Delete`, each write through `mutate` with an audit row (`connector.create`, `connector.update` with the changed field names only, `connector.delete`); `Store.ConnectorConnections()` with `Put/Get/ForSubject/ForConnector/ClaimRefresh/SetTokens/SetStatus/Delete/DeleteForConnector/DeleteForSubject` and the three state functions. `Put` and `Delete` are audited (`connector.connect`, `connector.disconnect`); refresh, claim, state start and sweep are not (plumbing, added to the list in `write.go`).

## 5. HTTP API

All JSON, all through huma so they land in `ui/openapi.json`. "Session only" means `Security: authn.SessionOnly` plus `authn.NoTokens` in the handler and a path matcher added to `sessionOnlyRoutes` in `internal/gateway/authn/authn.go`, as plan decisions have.

Admin (`authn.RegisterAdmin`, platform admins)

| Route | Body | Answer |
|---|---|---|
| `GET /api/admin/connectors/catalog` | | `{items: [CatalogEntry]}` |
| `GET /api/admin/connectors` | | `{items: [AdminConnector]}` |
| `POST /api/admin/connectors` | `ConnectorInput` | 201 `AdminConnector` |
| `GET /api/admin/connectors/{id}` | | `AdminConnector` |
| `PUT /api/admin/connectors/{id}` | `ConnectorInput` with `rev` | `AdminConnector`; 409 on a stale `rev` |
| `DELETE /api/admin/connectors/{id}` | | 204; revokes and deletes its connections first |
| `POST /api/admin/connectors/{id}/tools/refresh` | | `AdminConnector` with a new `tools` snapshot; uses the shared connection, or the admin's own |
| `POST /api/admin/connectors/{id}/connect` (session only, `shared` mode) | `{}` | `{authorizeUrl}` |
| `DELETE /api/admin/connectors/{id}/connection` (session only, `shared` mode) | | 204 |

```json
// ConnectorInput
{
  "catalogId": "slack",
  "slug": "slack",
  "name": "Slack",
  "url": "https://mcp.example.org/mcp",
  "authMode": "user",
  "enabled": true,
  "oauth": {
    "clientId": "client-1",
    "clientSecret": "write-only; leave out to keep the stored one",
    "authorizeUrl": "", "tokenUrl": "", "revokeUrl": ""
  },
  "scopes": ["channels:read", "chat:write"],
  "allowedGroups": ["sales", "*"],
  "toolModes": {"list_channels": "read", "post_message": "write"},
  "rev": "only on PUT"
}

// AdminConnector: ConnectorInput without clientSecret, plus
{
  "id": "c-1", "rev": "r-1",
  "oauth": {"clientId": "client-1", "secretStored": true, "registration": "static"},
  "tools": [{"name": "post_message", "description": "Post a message", "readOnlyHint": false}],
  "redirectUri": "https://tapas.example.org/settings/connections/callback",
  "shared": {"connected": true, "connectedBy": "alice", "status": "active"},
  "connections": 12,
  "updatedAt": "2026-11-26T10:00:00Z"
}

// CatalogEntry
{"id": "slack", "name": "Slack", "url": "https://mcp.example.org/mcp", "authMode": "user",
 "scopes": ["channels:read"], "toolModes": {"post_message": "write"}, "oauthEndpoints": "discovered",
 "setup": "Create an OAuth app at the vendor, add the redirect URI shown below, paste its client id and secret."}
```

Member

| Route | Who | Body | Answer |
|---|---|---|---|
| `GET /api/connectors` | any signed-in caller, tokens included | | `{items: [MyConnector]}`: only enabled connectors `ActUseConnector` allows |
| `POST /api/connectors/{id}/connect` | session only, `user` mode | `{}` | `{authorizeUrl}` |
| `POST /api/connectors/oauth/complete` | session only | `{state, code, iss?}` | `{connector: MyConnector}` |
| `DELETE /api/connectors/{id}/connection` | session only | | 204 |

```json
// MyConnector
{"id": "c-1", "slug": "slack", "name": "Slack", "authMode": "user",
 "connected": true, "status": "active", "connectedAt": "2026-11-26T10:00:00Z",
 "connectUrl": "https://tapas.example.org/settings/connections",
 "tools": [{"name": "slack__post_message", "mode": "write"}]}
```

Errors use the existing problem answers and new catalog messages in `internal/reasons/messages.go` (each must be emitted somewhere, see `emitted_test.go`): `ConnectorNotFound`, `ConnectorsOff`, `ConnectorSlugTaken`, `ConnectorURLNotAllowed`, `ConnectorSecretForNewAddress`, `ConnectorNotConnected`, `ConnectorSignInFailed`, `ConnectorSignInExpired`, `ConnectorNeedsOAuthClient`, `ConnectorToolNotFound`, `ConnectorDeniedByPolicy`, `ConnectorNotForAppTokens`, `ConnectorSessionOnly`. A caller who may not use a connector gets 404 `ConnectorNotFound`.

The routes are always registered in a non-local build (so `make openapi` sees them); with connectors off they answer 409 `ConnectorsOff`, as `/api/admin/ai` does.

## 6. Connect your account (OAuth)

Start (`POST .../connect`), in `oauth.Service.Start(ctx, connector, person, subject)`:

1. Authz: `user` mode needs `ActUseConnector`; `shared` mode needs platform admin.
2. Resolve endpoints. Static URLs on the row win. Otherwise: `GET` the MCP URL unauthenticated, read `resource_metadata` from `WWW-Authenticate` (`oauthex.ParseWWWAuthenticate`), else the well-known paths; `oauthex.GetProtectedResourceMetadata` (it checks `resource` equals the MCP URL); take the first authorization server; `auth.GetAuthServerMetadata`. Refuse when `code_challenge_methods_supported` lacks `S256`. Cache endpoints in memory per connector `rev` for 1 hour.
3. Resolve the client (D6).
4. Make `state` (32 random bytes, base64url) and a PKCE verifier (`oauth2.GenerateVerifier`). Store the state row (D8).
5. Answer the authorize URL: `response_type=code`, `client_id`, `redirect_uri` (exactly `{publicURL}/settings/connections/callback`, built from the configured public URL, never from the request's `Host`), `scope`, `state`, `code_challenge`, `code_challenge_method=S256`, and `resource` when discovered.

Complete (`POST /api/connectors/oauth/complete`), in `oauth.Service.Complete`:

1. Hash `state`, take the row (single use). Missing: `ConnectorSignInExpired`.
2. Refuse when `person` is not the session's person, when it expired, or when the connector's `rev` moved. The row is already gone, so a refused attempt cannot be retried.
3. When the authorization server advertised `authorization_response_iss_parameter_supported`, `iss` must equal the stored issuer (RFC 9207).
4. Exchange the code at the token endpoint with the verifier, the same `redirect_uri`, and `resource` when discovered (`oauth2.Config.Exchange` over the guarded HTTP client).
5. `vault.Put`: seal access and refresh tokens for a new row id, upsert, audit `connector.connect`.

Use (`vault.Token(ctx, connector, subject) (bearer string, err error)`):

- Returns the opened access token when `resource` still equals the connector's URL and it does not expire within 60 s.
- Otherwise refreshes: an in-process mutex per connection id, then `ClaimConnectorRefresh` for 30 s across replicas. The winner refreshes and writes with `SetConnectorTokens` (guarded by `rev`); a loser re-reads the row for up to 10 s and uses the new token. A rotated refresh token replaces the stored one.
- `invalid_grant`, or no refresh token: status `needs_reauth`, error `ConnectorNotConnected` carrying the connect URL. Audited as `connector.refresh_failed`.
- The broker calls `vault.Invalidate` and retries once when the connector answers 401.

Disconnect: open the tokens, call the revocation endpoint (RFC 7009) for the refresh token then the access token when one is advertised or configured (best effort, 5 s, outcome in the audit detail), then delete the row whatever revocation said. Deleting a connector, and removing a person (`internal/cli/usercmd`), do the same for every affected connection.

In-cluster connectors (architecture decision 4): the row has static OAuth URLs for the vendor and an in-cluster MCP URL. The broker passes the person's vendor token as `Authorization: Bearer` on each MCP request; the server keeps nothing. `client.Forwarder` takes the bearer as an argument per call, so nothing else changes.

## 7. Broker flow

Interfaces

```go
// internal/connectors
type Caller struct {
    Principal authz.Principal // the token's owner, from the store
    Roles     []authz.Role
    Token     token.Token     // which token; Token.App names an agent's app
    Agent     string          // the coding agent app's short name, "" for a personal token
    AgentApp  bool            // the token's app is a coding agent
    Client    string          // what the MCP client calls itself; display only
}

// internal/mcp (Config.Broker; nil means no connectors)
type Broker interface {
    Tools(ctx context.Context, c connectors.Caller) []*sdk.Tool
    Call(ctx context.Context, c connectors.Caller, name string, args json.RawMessage) (*sdk.CallToolResult, bool)
}

// internal/connectors/client
type Target struct{ URL string }
type Forwarder interface {
    ListTools(ctx context.Context, t Target, bearer string) ([]*sdk.Tool, error)
    CallTool(ctx context.Context, t Target, bearer, name string, args json.RawMessage) (*sdk.CallToolResult, error)
}

// internal/connectors/policy
type Input struct {
    Phase     string // "read", "propose" or "apply"
    Person    string
    Groups    []string
    Agent     string
    TokenID   string
    Connector string
    Tool      string
    Write     bool
    Arguments json.RawMessage
}
type Decision struct{ Effect, Reason string } // allow, deny, require_approval
type Checker interface {
    Check(ctx context.Context, in Input) (Decision, error)
}
```

Identity. `authn.Authenticator` gains `LiveCaller(ctx, text) (token.Token, authz.Principal, AgentCaps, error)` (what `liveToken` already computes). `tokenVerifier` stores a `connectors.Caller` in `auth.TokenInfo.Extra["caller"]`. The middleware reads it from `req.GetExtra().TokenInfo`. Nothing in the tool call's arguments or headers can name another person or agent.

`tools/list`. For each enabled connector the caller passes `ActUseConnector` for and has a connection to (their own, or the shared one): cached upstream tools, minus `off` tools, renamed `<slug>__<tool>`, description prefixed with the connector's name, `readOnlyHint` set from `tool_modes` (not from upstream), and for write tools the sentence the existing propose tools use ("Proposes the change only...").

`tools/call` of `<slug>__<tool>`:

1. Caller kind (D15). Refused: tool error, audit `denied_token`.
2. Connector by slug, enabled. Unknown: tool error "no such tool" (same text as a denied one).
3. Authz: `authz.Decide(p, roles, ActUseConnector, Object{Connector: ...})`. Denied: same "no such tool", audit `denied_authz`.
4. Tool exists in the cached list and is not `off`. Arguments are a JSON object of at most 64 KiB.
5. Mode from `tool_modes` (missing is `write`).
6. Read: `policy.Check(Phase: "read")`. Deny: tool error with the reason, audit `denied_policy`. `require_approval`: treat as a write from here.
7. Write: in-process `POST /api/plans` with the caller's `Authorization` header and `{source: "mcp:<client>", proposal: {action: "connector-call", target: "<slug>__<tool>", params: {"arguments": "<canonical JSON>"}}}`. The action's `Plan` repeats steps 1 to 5, runs `policy.Check(Phase: "propose")`, requires a usable connection, and returns `Planned{Summary: "Slack: post_message", Changes: one per top-level argument (field, "", value), Destructive: upstream destructiveHint}`. The tool answers `{planId, state: "awaiting_approval", approveUrl}`; audit `proposed`. Nothing is forwarded.
8. Token: `vault.Token`. Not connected: tool error with the connect URL, audit `not_connected`.
9. Forward: `Forwarder.CallTool` with the upstream tool name and the arguments unchanged, under the 60 s deadline. A 401 refreshes and retries once.
10. Audit (D19), then return the connector's result as it came, capped at 1 MiB.

Apply after approval (`connector-call` `Apply`, called by `plans.Approve` in the approver's browser session): parse the stored target and arguments; repeat connector enabled, authz for the plan's owner, tool still present and not `off`; `policy.Check(Phase: "apply")` where anything but `allow` fails the plan; token; forward exactly the stored arguments; a tool result with `isError` is returned as an error so the plan becomes `failed`. The plan's audit row (`ActionApplied` or `ActionFailed`) gets kind `connector`, the slug as name, and the tool in its detail; the broker also writes its own `connector.call` row.

Audit row for a call: `actor` and `via` from `model.Actor{Person, Token}` (`store.ActorStrings`), `action: "connector.call"`, `object_kind: "connector"`, `object_name: <slug>`, `object_id: <connector id>`, `object_owner: <person>`, `allowed`, and `detail`:

```json
{"tool": "post_message", "mode": "write", "outcome": "ok", "agent": "helper-1", "client": "claude-code",
 "credential": "user", "plan": "<plan row id, writes only>", "policy": "allow", "ms": 412}
```

`outcome` is one of `ok`, `tool_error`, `proposed`, `denied_token`, `denied_authz`, `denied_policy`, `not_connected`, `upstream_error`, `timeout`. No arguments, no result content, no token, no upstream error body (only its class).

## 8. Security requirements

Each has a test named in section 9.

1. Tokens and the client secret are sealed at rest (Tink AEAD, sessions key), bound to row and column. A ciphertext moved to another row or column does not open. Connectors cannot be enabled with an ephemeral sessions key.
2. No vendor token, client secret, PKCE verifier, state or authorization code is ever logged, audited, returned by an API, or passed to an agent. Types that hold them implement `slog.LogValuer` and `fmt.Formatter` to print `[redacted]`. Errors from token endpoints are reduced to their OAuth error code before they are wrapped.
3. PKCE S256 always; refuse an authorization server that does not advertise it. State is 256 random bits, stored hashed, single use, expires in 10 minutes, and is bound to the person who started it and to the connector `rev`.
4. Redirect URI is one exact string per deployment, built from configuration; it is stored with the state and sent unchanged at exchange. The admin page shows it for pasting at the vendor.
5. SSRF. Admin-supplied and discovered URLs (MCP, metadata, authorize, token, registration, revocation) must be `https`. Every outbound request uses one guarded HTTP client: the address actually dialed (after DNS) may not be loopback, link-local, unspecified, multicast or a cloud metadata address, and may not be private (RFC 1918, ULA, carrier-grade NAT) unless the URL's host is listed in the platform config `connectors.internalHosts` (how in-cluster connectors are allowed, and the only place plain `http` is accepted). No redirects are followed. Response bodies are capped. The dial guard is moved from `internal/ai/aisettings` into `internal/httpx` and shared, not copied.
6. A token is sent only to the origin it was issued for (`connector_connections.resource` must equal the connector URL); D20 deletes connections when that changes.
7. Identity comes only from the verified Tapas token. Arguments are forwarded opaque and never read for identity.
8. Fail closed: an error from authz inputs, the policy hook, the vault, the audit write, or an unknown tool mode refuses the call. Unknown tools are writes.
9. Only a browser session can connect, disconnect, complete OAuth, or approve. Tokens and agents cannot.
10. Tool descriptions and results from a connector are untrusted text. The broker does not interpret them; it only caps their size.
11. Admin changes to connectors are audited with field names, never values.

## 9. Test plan

Rules: tests first, behaviour through public interfaces, placeholder data only (`alice`, `bob`, `alice@example.com`, hosts under `example.org`, ids like `c-1`), no real vendor names in fixtures beyond catalogue ids.

Fakes, in `internal/connectors/connectorstest` (no network beyond `httptest`):

- `NewAuthServer(t)`: an `httptest.Server` serving RFC 8414 metadata, `/authorize` (records the query, a helper returns the redirect it would make), `/token` (checks the PKCE verifier against the challenge, `redirect_uri`, `resource`, client auth; issues access and refresh tokens with a set lifetime; supports `refresh_token` with rotation and a switch to answer `invalid_grant`), `/register` (RFC 7591, optional), `/revoke` (records calls, optional). Knobs turn features off to test fallbacks.
- `NewConnector(t, as)`: a go-sdk `mcp.Server` behind `mcp.NewStreamableHTTPHandler` and `auth.RequireBearerToken` (verifier asks the fake authorization server), serving RFC 9728 metadata. It has a read tool `echo` and a write tool `post`, records every call with the bearer it saw, and can be told to answer 401, hang, or return an oversized result.
- Tests reach these loopback servers through an unguarded HTTP client injected into the services; the guard has its own tests.

Store tests use `storetest.Open` and run on both engines. Gateway tests use `internal/gateway/apitest` and `gatewaytest`, as `mcp_test.go` does.

| Slice | Tests written first |
|---|---|
| 1 | Store: create, get by slug, list, update with a stale `rev` conflicts, delete; each write leaves one audit row with field names only. Catalog: every entry validates (slug pattern, `https` URL, modes), ids unique. Registry: the client secret round-trips sealed and is never in a returned view; changing the URL origin without a new secret is refused. API: a member gets 403 on every admin route; create from a catalogue entry; duplicate slug is 409; bad URL is 422. |
| 2 | Client: lists and calls the fake connector; the bearer given per call is the one the connector saw; hang hits the deadline; oversized result is cut; a redirect is not followed. Guard: private, loopback and metadata addresses are refused, a host in `internalHosts` is allowed. MCP: `tools/list` through `/mcp` shows `fake__echo` next to Tapas's own tools; `tools/call fake__echo` returns the connector's answer; a disabled connector's tools are gone; Tapas's own tool names contain no `__`. |
| 3 | OAuth: full code flow against the fake server, with and without discovery, with and without dynamic registration; wrong person completing is refused and the state is spent; a second use of a state fails; expired state fails; `iss` mismatch fails; no S256 is refused. Vault: tokens are not readable in the table; a ciphertext swapped between rows does not open; refresh on expiry; rotation stored; two concurrent callers cause one refresh; `invalid_grant` gives `needs_reauth`; disconnect calls `/revoke` then deletes, and deletes when `/revoke` fails; shared mode uses the one row for bob after alice (admin) connected. API: connect and complete are refused for a bearer token. |
| 4 | Authz table test for `ActUseConnector`: group match, `*`, empty list, disabled connector, anonymous, admin without the group. MCP: bob outside the group sees no `fake__` tools and a call answers the same error as an unknown tool; a plain app token is refused, an agent app token is allowed. `GET /api/connectors` lists only what the caller may use. |
| 5 | Policy: a checker that denies `post` stops the call before the connector (zero recorded calls) and the reason reaches the caller; a checker that errors denies; `require_approval` on a read produces a plan; the input holds person, groups, agent, connector, tool and arguments. |
| 6 | Calling `fake__post` records no connector call and returns a plan id; the plan shows the exact arguments; approve applies once (one recorded call, arguments byte-equal); second approve is 409; reject and expiry reach the connector zero times; a connector disabled between propose and approve fails the plan; a read token cannot propose; a token cannot approve. |
| 7 | Each outcome in section 7 writes exactly one row with the expected fields; a sentinel string placed in arguments, in the result and as the access token appears nowhere in `audit_events` or in captured `slog` output; when the audit append fails the tool answers an error and the result is withheld. |
| 8 | Vitest with `mockApi` and `renderApp`: admin list, enable from catalogue, secret field is write-only, tool mode toggles save, stale `rev` shows the conflict; member list, Connect navigates to `authorizeUrl`, the callback page posts `state` and `code` once and shows success or the error, Disconnect asks to confirm; non-admins do not see the admin tab. |

## 10. Slices

Every slice is one small commit series that builds, passes `go vet ./...`, `gofmt -l .` (empty), its tests on SQLite, and for slices that touch `internal/store` also on Postgres. Common files that several slices edit are listed under "Shared files".

### Slice 1: registry and catalogue

- Files: `internal/store/migrations/00028_connectors.sql`, `internal/store/queries/connectors.sql`, `internal/store/connectors.go` (+ test), generated `internal/store/sqlcgen/*`; `internal/connectors/connectors.go` (types, validation); `internal/connectors/catalog/{catalog.go,catalog.json}`; `internal/connectors/registry/registry.go`; `internal/gateway/connectors/admin.go`; `internal/gateway/connectors.go` (`!local`, `mountConnectors`) and `connectors_local.go` (no-op); one line in `internal/gateway/server.go`; `Config.Connectors` and config key `connectors.enabled`, `connectors.internalHosts` in `internal/config`; wiring in `internal/cli/servercmd`; `internal/reasons/messages.go`; `ui/openapi.json` via `make openapi`.
- Catalogue entries: `slack`, `github`, `notion`, `hubspot`, `google-gmail`, `google-calendar`, `google-drive`. Each entry's MCP URL, scopes and tool list must be checked against the vendor's own documentation when the entry is written; an entry whose hosted MCP URL cannot be confirmed ships with an empty URL the admin fills in (for Google, the in-cluster servers of #5 with Google's OAuth endpoints as static URLs).
- Done: an admin can create, edit, enable and delete connectors through the API on both engines; the secret is sealed; nothing calls out yet.

### Slice 2: broker forwarding

- Files: `internal/httpx/guard.go` (moved from `aisettings`, which now calls it); `internal/connectors/client/client.go`; `internal/connectors/connectorstest/connector.go`; `internal/connectors/broker/broker.go` (list and read-forward only, every tool treated as a read, bearer from a `TokenSource` interface with a static test implementation); `internal/mcp/broker.go` (the `Broker` interface and middleware) and two lines in `internal/mcp/server.go`; `internal/gateway/mcp.go` (pass the broker; `LiveCaller`); `internal/gateway/authn/authn.go` (`LiveCaller`).
- Done: with a connector row pointing at the fake, an MCP client lists and calls its tools through `/mcp`. No auth to the connector beyond a fixed test bearer. `make test-local` still passes (no go-sdk in the local build).

### Slice 3: per-user OAuth and vault

- Files: `internal/store/migrations/00029_connector_connections.sql`, `internal/store/queries/connector_connections.sql`, `internal/store/connector_connections.go`, `sqlc.yaml` overrides, generated code; `internal/connectors/vault/vault.go`; `internal/connectors/oauth/{discover.go,flow.go,revoke.go}`; `internal/connectors/connectorstest/authserver.go`; `internal/gateway/connectors/member.go`; session-only matcher in `internal/gateway/authn/authn.go`; janitor start in `internal/cli/servercmd/gateway.go`; connection cleanup in `internal/cli/usercmd/user.go`; broker's `TokenSource` now the vault.
- Done: alice connects through the fake authorization server, her calls reach the fake connector with her token, the token refreshes, disconnect revokes and deletes; shared mode works.

### Slice 4: authz action

- Files: `internal/authz/authz.go` (+ `connectors_test.go`): `ActUseConnector`, `Object.Connector *ConnectorAsk{Enabled bool; Groups []string}`; broker and `GET /api/connectors` call `Decide`; caller-kind rule (D15).
- Done: listing and calling honour groups; outsiders cannot tell a connector exists.

### Slice 5: policy hook

- Files: `internal/connectors/policy/policy.go` (+ test); broker calls it; `broker.Config.Policy` defaults to `policy.AllowAll{}`.
- Done: a test checker can deny or require approval; errors deny. (`require_approval` on a read is asserted in slice 6's tests if slice 6 lands later; until then it denies.)

### Slice 6: writes through plans

- Files: `internal/actions/proposal.go` (const `ConnectorCall = "connector-call"`), `internal/actions/catalog.go` (hidden actions left out of `Describe`); `internal/connectors/broker/action.go` (the `actions.Action`); `internal/gateway/apps/workspaces.go` (`Config.ExtraActions`), `internal/gateway/apps/plans.go` (audit kind for connector plans); broker write path; `ui/src/features/approvals/copy.tsx` (wording for the new action).
- Done: issue #4's definition of done, against the fake connector.

### Slice 7: audit

- Files: `internal/connectors/broker/audit.go`; redaction types in `internal/connectors`; comment list in `internal/store/write.go`.
- Done: every outcome has one row; the sentinel test passes.

### Slice 8: UI

- Files: `ui/src/features/settings/admin/AdminConnectors.tsx` (+ test), `ui/src/features/settings/Connections.tsx`, `ConnectionCallback.tsx` (+ tests), hooks in `ui/src/features/settings/admin/queries.ts` and `ui/src/lib/queries.ts`, routes in `ui/src/app/App.tsx`, nav entries in `ui/src/features/settings/Settings.tsx`, canned answers in `ui/mock/main.ts`, regenerated `ui/src/api/*` (`pnpm gen:api`).
- Admin screen "Connectors" (pattern: `AdminAI.tsx`): list with status; "Add" opens the catalogue or "Custom MCP URL"; form with the redirect URI to copy, client id, write-only secret, scopes, groups, and a tools table with a read/write/off control per tool and "Refresh tools".
- Member screen "Connected accounts" (pattern: `Secrets.tsx`): one card per connector with Connect or Disconnect, status, and "Reconnect" when `needs_reauth`.
- Done: `pnpm -C ui test` and `pnpm -C ui build` pass; the flow works in `pnpm mock`.

### Order and parallel work

Order: 1, then 2, then 3 to 8.

| Slice | Needs | Can run in a parallel worktree with |
|---|---|---|
| 1 | nothing | 4a (the `authz.go` change and its table test alone), 5a (`policy` package alone) |
| 2 | 1 | 3-store (migration 00029, queries, store code, vault), 8-admin (admin screen against slice 1's OpenAPI) |
| 3 | 1, 2 | 8-admin |
| 4 (broker wiring) | 2 | 5, 7 only if sequenced: all three edit `broker/broker.go` |
| 5 (broker wiring) | 2 | as above |
| 6 | 3, 4, 5 | 8-member |
| 7 | 2 (final pass after 6) | 8 |
| 8 | 1 (admin), 3 (member) | all backend slices |

Shared files and how to avoid conflicts

- Migration numbers are fixed above: 00028 (slice 1), 00029 (slice 3). No other slice adds one. If `main` takes 00028 first, renumber both in one commit before merging.
- `internal/store/sqlcgen/*` is generated: never merge it by hand. After a rebase run `make generate-sql` and commit the result.
- `internal/store/sqlc.yaml`: only slice 3 edits it.
- `internal/gateway/server.go`: slice 1 adds the single `mountConnectors(...)` line and the `Config.Connectors` field. Later slices change only `internal/gateway/connectors.go` and the `internal/gateway/connectors` package.
- `internal/connectors/broker/broker.go`: slice 2 lays out the pipeline as named steps (`identify`, `authorize`, `checkPolicy`, `propose`, `token`, `forward`, `record`), each a method in its own file with a pass-through body. Slices 4 to 7 each fill one file, so they do not touch the same lines.
- `internal/reasons/messages.go`: slice 1 adds all the messages in section 5 in one block, with a test that emits them, so later slices do not edit the file.
- `ui/openapi.json` and `ui/src/api/*` are generated: on conflict take either side, then rerun `make openapi` and `cd ui && pnpm gen:api`.
- `internal/gateway/authn/authn.go`: slice 2 adds `LiveCaller`, slice 3 adds the session-only matcher. Different functions; keep them in that order.

## 11. Toolchain notes

- No Docker in this sandbox, so `make test`, `make test-db` and `make test-postgres` (which start Postgres in a container through `scripts/test/postgres-test.sh`) do not run here. Run Postgres tests against a Postgres provided locally: `JAVO_TEST_DB=postgres JAVO_TEST_PG_DSN='postgres://...' go test -count=1 ./internal/store/...`. In this sandbox a Postgres 17.11 from the checksum-verified `io.zonky.test.postgres:embedded-postgres-binaries-linux-amd64:17.11.0` jar on Maven Central runs on a loopback port with a random password. When no DSN is available, say so in the slice's status instead of claiming Postgres passed.
- SQLite is the default: `go test ./internal/store/... ./internal/connectors/... ./internal/mcp/... ./internal/gateway/...`. Some packages under `internal/controller` need envtest binaries; they are not touched by this plan.
- Lint is `go vet ./...` and `gofmt -l .`. Also run `make test-local` after slices 1, 2 and 3: it proves the `local` build still links neither the go-sdk nor the new SDK-importing packages (add `internal/connectors/{client,oauth,broker}` to the list in `test/localbuild/localbuild_test.go`).
- `make generate-sql` needs `bin/sqlc`, fetched at the pinned version and checksum by `scripts/build/fetch-sqlc.sh` (`make bin/sqlc` with the absolute path, as the Makefile names it).
- UI: `pnpm -C ui install --frozen-lockfile --ignore-scripts`, `pnpm -C ui test`, `pnpm -C ui build`. No new packages, so `ui/pnpm-lock.yaml` does not change.
- No new dependency is needed (D21). If one becomes unavoidable, CONTRIBUTING.md applies: exact pin, advisory and install-script check, and a row in `DEPENDENCIES.md`.

## 12. Open points to confirm while building

- D9: whether `.` is accepted as a tool-name character by every MCP client we target. The plan assumes not and uses `__`.
- Catalogue URLs and scopes per vendor (slice 1). They are data, so a wrong one is a one-line fix, but none should ship unverified.
- Plans expire after 10 minutes (`plans.DefaultTTL`). That is short for a person who is away. Changing it is outside this plan; raise it under #4.
- Whether the applied write's result should be shown to the agent (non-goal here).

## Status

As of 2026-11-26, on branch `connectors-broker`.

### Done

- Slice 1: the `connectors` table (00028), the built-in catalogue (Slack, GitHub, Notion, HubSpot, Gmail, Google Calendar, Google Drive), the registry with the OAuth client secret sealed by the sessions key, and the admin API. Every catalogue address and scope was checked against the vendor: each address answers as an MCP server (401 with OAuth metadata, or Google's with its own OAuth) and is named in the vendor's docs; every scope is in the vendor's docs or, for GitHub's `read:org`, in its server's `scopes_supported`.
- Slice 2: the broker on `/mcp`: tools listed per caller as `<slug>__<tool>`, reads forwarded with the person's token, one MCP session per call, the guarded HTTP client shared with the AI settings.
- Slice 3: per-person and company-wide OAuth (RFC 9728 and 8414 discovery, PKCE S256, RFC 7591 registration, RFC 8707 resource, RFC 9207 iss), the token vault (00029) with refresh, rotation and revocation, the member and admin routes.
- Slices 4 and 5: `ActUseConnector` in authz and the policy hook (`policy.Checker`, allow-all by default, any error denies), both on every call.
- Slice 6: writes, and reads a policy sends for approval, become plans holding the exact arguments; the owner approves in the browser and the stored call is sent once, after the checks run again.
- Slice 7: one `connector.call` audit row per call, proposal and approved call, with names and outcomes only; a call whose row cannot be written answers an error and withholds the result.
- Slice 8: Settings, Connectors (every catalogue entry listed with its vendor's logo) and Settings, Connected accounts, the sign-in callback page, the approval card for connector calls.
- Docs: `site/src/content/docs/admin/connectors.md` and the connector section of `using/ask-ai.md`.

Shared-account mode removed (replaces D4): every person connects their own account, and an admin can no longer connect one account that everyone's agents act through. Connectors have no auth mode in the code, the catalogue or the API; the admin routes that connected and disconnected that one account are gone, and Refresh tools uses the admin's own connection. Migration 00031 deletes the connections with no person (subject `''`) and the sign-ins started for them, turns off every connector that was in that mode so an admin reviews it, and drops the `auth_mode` column and the OAuth state's `subject` column. The store never stores or reads a connection with no person, so a stray row is never used.

Deviations from the plan above, made while building and recorded in the commits: tool names keep `__` (D9 is still open); the redirect address is one per connector, `{publicURL}/settings/connections/callback/{slug}` (D7, after the security review); a connector's authorization server is pinned at its first sign-in (migration 00030) and a change counts as a move (D20); a plan records the connector's id and address and fails if either changed.

### Not done

- Real policies: the OPA checker of #3. Only the interface and the allow-all default ship.
- A NetworkPolicy so in-cluster connectors accept traffic only from the gateway (#2; needs enforcement on EKS).
- Our own in-cluster MCP servers (Greenhouse, Gmail send). The broker passes the person's token per call, so they need no code here.
- Plans still expire after 10 minutes (`plans.DefaultTTL`), short for a person who is away.
- `javo user remove` deletes a person's connections without revoking them at the vendor (the CLI has neither the sessions key nor the guarded client).
- An agent sees whether an approved write was applied or failed, not the connector's answer.

### Run it locally

- Tests: `make test` (needs Docker for the Postgres pass), or without Docker `JAVO_TEST_DB=postgres JAVO_TEST_PG_DSN=postgres://... go test ./internal/store/...` and `go test ./...`; `make test-local`; `pnpm -C ui test` and `pnpm -C ui build`.
- The UI alone: `pnpm -C ui mock` shows both screens, a fake vendor sign-in included.
- A cluster: `make up`, then set `connectors.enabled: true` in the platform config and add a connector in Settings, Connectors. The fakes in `internal/connectors/connectorstest` (an MCP server and an OAuth authorization server) are what the tests run against.

### Security review

Two independent reviews ran, each by a separate agent given the code and the requirements, not conclusions.

The first (everything before slice 6) found no token leak, no cross-person token use and no authz bypass. It found: the authorization server was not pinned after sign-in (medium), an OAuth mix-up through the one shared redirect address (medium), a connection stored across a connector change, no total size cap on tool lists, a same-origin path change keeping connections, IPv6 forms that embed an IPv4 address passing the address guard, arguments read differently by the policy and the connector, and a shared connection revived by switching modes back (low or informational). All are fixed at the root, each with a test that failed first: pinning in 00030, per-connector redirect addresses and a strict iss check, a revision guard on storing a connection, byte caps on lists and on the broker's cache, exact address comparison, the guard reading embedded IPv4, refusing arguments that name a field twice, and an auth mode change counting as a move.

The second (slice 6) found no critical or high issue; ownership, approval, apply-once and the hidden action held. It found: field names that differ only by case or a look-alike letter, and invisible or direction characters on the approval card (medium); an approved call reaching a connector moved since it was asked, and a connector's hints dropping the undo warning on a write tool (low); and a wrong message for a browser session. All are fixed, each with a failing test first.

Known and accepted: with static OAuth endpoints (the Google entries) an `iss` in the answer is ignored rather than refused, since static endpoints have no known issuer; the per-connector redirect address keeps those answers apart.
