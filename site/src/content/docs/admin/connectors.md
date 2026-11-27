---
title: Connectors
description: Optional. Let people's agents use Slack, GitHub, Notion, HubSpot, Google and other MCP servers through Tapas, with their own accounts, without ever seeing a token.
sidebar:
  order: 10.6
---

A connector is an MCP server of another service, such as Slack or GitHub. Agents never call it themselves: they call Tapas's own MCP endpoint (`/mcp`), and Tapas checks the call, uses the person's connected account, and records it. Agents never see a password or a token.

## Turn connectors on

In the platform config (the chart's `config:` values):

```yaml
connectors:
  enabled: true
```

Connectors need the gateway's sessions key to survive restarts (`--sessions-key-secret`, which the chart sets): the OAuth client secrets and people's tokens are encrypted with it. The gateway refuses to start with connectors on and a key made at start.

## Add a connector

**Settings, Connectors** lists the built-in catalogue (Slack, GitHub, Notion, HubSpot, Gmail, Google Calendar, Google Drive) and the connectors you added. For each:

1. Click **Set up**. The form is filled in from the catalogue; for an MCP server of your own, use **Add a custom MCP server**.
2. At the vendor, create an OAuth app and give it the **redirect address** the form shows. Each connector has its own address, `https://<tapas host>/settings/connections/callback/<short name>`; the vendor must have it exactly.
3. Paste the OAuth client id and secret. The secret is never shown again; leave the field empty to keep it. Some servers (such as Notion's) register a client themselves: leave both empty.
4. Choose the **allowed groups**. `*` means every member; an empty list means nobody. Being a platform admin does not let you use a connector.
5. Mark each tool **read**, **write** or **off**. Reads run straight away; writes (and any tool not marked) become a request the person approves in the browser; off tools are not offered. **Refresh tools** reads the server's current list, signed in with your own account, so connect yours first.

Every person connects their own account in **Settings, Connected accounts**, and their agents act as them at the vendor. There is no account an admin connects for everyone.

Changing a connector's address or OAuth client disconnects everyone (their tokens are revoked at the vendor), and a new address or client needs the secret again. If the server's sign-in service changes, Tapas stops new sign-ins until you save the connector again.

## MCP servers inside the cluster

Connector addresses must be `https` and public. To use an MCP server running in the cluster, list its host name, with no scheme or port:

```yaml
connectors:
  enabled: true
  internalHosts: [mcp-greenhouse.tapas.svc]
```

Tapas sends such a server the person's token with each call; the server keeps nothing.

Not yet done: a NetworkPolicy so that in-cluster connectors accept traffic only from the gateway (it needs NetworkPolicy enforced on the cluster).

## What is recorded

Every call through a connector, every proposed write and every approved one adds a row to the audit log with the person, the agent, the connector, the tool and the outcome. Never the values sent, the answer or a token.
