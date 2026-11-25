---
title: Memory
description: Optional. Connect Tapas to the mem0 pack so the gateway can keep memory for people's agents. Today this is setup and a start check only.
sidebar:
  order: 10.5
---

Memory is kept by [mem0](https://github.com/mem0ai/mem0), which the [mem0 pack](https://github.com/nebari-dev/mem0-pack) runs in its own namespace. With memory on, the gateway builds the memory service at start and checks that mem0 answers.

## What memory does today

This release does the setup only. The gateway reads mem0's key, builds the client and logs at start whether mem0 answers. It does not yet put memories into prompts, learn from answers or offer a memories API; those come in later releases. Turning memory on today changes nothing people see.

Off (the default), the gateway never talks to mem0 and never reads its key.

## Install the mem0 pack with Tapas as its caller

The mem0 pack has no published chart yet (nebari-dev/mem0-pack#12), so install it from a checkout of the repository. Its [README](https://github.com/nebari-dev/mem0-pack#readme) lists the prerequisites (a CNI that enforces NetworkPolicy, CloudNativePG, llm-serving-pack and a Keycloak client for mem0) and the full install steps. Only what Tapas needs is repeated here.

The pack admits only the pods you list under `callers`, and lets only the ServiceAccounts you list read its admin key Secret. List the Tapas gateway:

```yaml
callers:
  - namespace: javo-system            # the namespace of the Tapas release
    serviceAccount: javo-controller   # javo-gateway when Tapas uses Postgres
    podSelector:
      app.kubernetes.io/name: javo
      app.kubernetes.io/component: gateway
```

- `namespace` is the namespace Tapas is installed in (`javo-system` in these pages' examples).
- `serviceAccount` depends on Tapas's `database.engine`. With SQLite (the default) the gateway and the controller run in one pod, as `javo-controller`. With Postgres the gateway runs as `javo-gateway`.
- The pod labels are the same for both engines: the gateway pod carries `app.kubernetes.io/name: javo` and `app.kubernetes.io/component: gateway`.

Install mem0 first, then turn memory on in Tapas. Helm prints mem0's in-cluster address when the install finishes, for example `http://mem0-nebari-mem0.mem0.svc:8000`.

## Turn it on in Tapas

```yaml
config:
  memory:
    enabled: true
    url: http://mem0-nebari-mem0.mem0.svc:8000
    timeout: 10s
    addTimeout: 120s
    apiKeySecret:
      namespace: mem0
      name: mem0-admin-key
      key: api-key
```

| Key | Default | Meaning |
|---|---|---|
| `enabled` | `false` | Turns memory on. |
| `url` | none | The mem0 pack's Service address, `http` or `https` with a host. Required when memory is on. |
| `timeout` | `10s` | How long a search, list, get or delete call may take. |
| `addTimeout` | `120s` | How long an add call may take. It is longer because mem0 runs model extraction inline. |
| `apiKeySecret.namespace` | none | The namespace of mem0's admin key Secret, which is the namespace the mem0 pack is installed in. Required when memory is on. |
| `apiKeySecret.name` | `mem0-admin-key` | The Secret's name. This is the pack's default; change it if you set `adminKey.secretName` or `adminKey.existingSecret` in the pack. |
| `apiKeySecret.key` | `api-key` | The key inside the Secret that holds the admin key. |

The chart passes this section into the config file as written and creates no Role in mem0's namespace. The gateway reads the key through the Role the mem0 pack creates for the accounts in `callers`.

The gateway refuses to start with memory on when `url` is missing or is not an `http` or `https` address, when `apiKeySecret.namespace` is missing or is not a valid namespace name, or when `executor` is not `kubernetes`.

## What the gateway logs at start

- Memory off: nothing about memory is logged.
- `memory.ready` with `url`: the gateway asked mem0 for the memories of a reserved name (`tapas-start-check`, which belongs to no person) and mem0 answered.
- `memory.unreachable` with `url` and `err`: mem0 did not answer correctly. The `err` text says why. `mem0 refused the key` means mem0 answered 401, so the key in the Secret is not the one mem0 uses (the Secret was replaced, or `apiKeySecret` names the wrong one). A message such as `GET /memories answered 503`, or a connection error, means mem0 is down, still starting or not reachable from the gateway, for example because the `callers` entry does not match the gateway pod. The gateway keeps running and does not retry; fix the cause and restart the gateway to check again.
- The start fails with `read the mem0 key Secret <namespace>/<name>` when the Secret is missing or the gateway may not read it (the `callers` entry is wrong). It fails with `Secret <namespace>/<name> has no "<key>"` when the Secret has no such key or its value is empty. The error never contains the key.

## Limits

- Memory needs the Kubernetes executor. With the Docker or process executor the gateway refuses to start when `memory` is set.
- The key is read once, at start. After the key changes (a new Secret, or a rotation in mem0), restart the gateway so it reads the new one.
- The gateway needs mem0's key Secret to exist when it starts, and fails to start until it does. Install the mem0 pack first, or expect the gateway pod to restart until the Secret appears.
- mem0 has no per-person access control: anything holding its admin key can read every person's memories. Tapas scopes each call to one person and keeps the key to the gateway, and the pack's NetworkPolicy lets only the pods in `callers` connect. Do not add other callers or share the Secret.
