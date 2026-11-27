---
title: Memory
description: Optional. Connect Tapas to the mem0 pack so the admin's agent model remembers what people tell their coding agents, and people can see and delete it under Settings > Memories.
sidebar:
  order: 10.5
---

Memory is kept by [mem0](https://github.com/mem0ai/mem0), which the [mem0 pack](https://github.com/nebari-dev/mem0-pack) runs in its own namespace. With memory on, the gateway builds the memory service at start, checks that mem0 answers, and gives the admin's agent model a memory of each person.

## What memory does

Memory works only on calls to the admin's agent model (`ai.agent`, see [AI](/admin/ai/)), which coding-agent apps such as pi reach through the gateway. Off (the default), the gateway never talks to mem0 and never reads its key.

- **Recall.** Before an agent's model call, the gateway searches the person's memories for their latest message (on every call, including the agent's steps after a tool result). It adds up to 5 matches (each cut to 500 characters, 2500 in all) to the call's system message as a note that tells the model to use them when relevant and not to mention the note. Results are kept for 2 minutes per person and query, so the repeated steps of one agent turn search once. A search waits at most `recallTimeout`; if it fails or runs out of time, the call goes on without memories.
- **Learning.** After a call that answers something the person typed, the gateway sends the typed text and the start of the answer (at most 4000 characters) to mem0 in the background, and mem0 keeps only lasting facts about the person: their name, role, team, skills, preferred tools and languages, how they like answers written, and standing instructions. Nothing is kept about the task, code, file contents, errors or command output. A step where the agent continues after a tool result, with no new typed text, is not learned from.
- **"Remember that ...".** A message that starts with `remember that ...` or `remember: ...` is stored as it is, without extraction. A sentence longer than 1000 characters is skipped.
- **Social Security numbers.** A write whose text holds a US Social Security number is refused; the number is never stored or logged.
- **The Memories page.** Each person sees their memories under **Settings > Memories**, newest first, and can delete any of them. Deleting also clears that person's cached recall results on that gateway. The page is served by `GET /api/memories` and `DELETE /api/memories/{id}`, which browser sessions and personal API tokens can call; an app's or a job's own token is refused with 403, except the token of a coding agent whose owner has let it act without asking. With memory off both answer 404 "Memory is off on this platform."; when mem0 cannot be reached they answer 502 "Memory could not be reached; try again later." and the gateway logs the cause.

A coding agent running on a person's own key (**Settings > Secrets > AI agent**) bypasses the gateway, so it gets no recall and learns nothing. Only the admin's agent model has memory. The suggestions in New app's **Describe it** box, Explain more and the other model features do not use memory either.

A call is also left alone, with no recall and no learning, when its first system message is pi's own summarization prompt ("You are a context summarization assistant.") or the typed text is longer than 8000 characters, such as a large paste.

Learning runs at most 4 calls at a time per gateway; a turn that finds all 4 busy is not learned from.

## Install the mem0 pack with Tapas as its caller

The mem0 pack is published as the chart `nebari-mem0`. A prerelease is installed only when you name its version, so install `--version 0.1.0-alpha.1` from the Nebari Helm repository (`helm repo add nebari https://raw.githubusercontent.com/nebari-dev/helm-repository/gh-pages/`, then `helm install mem0 nebari/nebari-mem0 --version 0.1.0-alpha.1 -n mem0 -f my-values.yaml`) or from `oci://quay.io/nebari/charts/nebari-mem0`. Its [README](https://github.com/nebari-dev/mem0-pack#readme) lists the prerequisites (a CNI that enforces NetworkPolicy, CloudNativePG, llm-serving-pack and a Keycloak client for mem0) and the full install steps. Only what Tapas needs is repeated here.

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
    recallTimeout: 2s
    apiKeySecret:
      namespace: mem0
      name: mem0-admin-key
      key: api-key
```

| Key | Default | Meaning |
|---|---|---|
| `enabled` | `false` | Turns memory on. |
| `url` | none | The mem0 pack's Service address, `http` or `https` with a host, and no user name, password, query or fragment. Required when memory is on. |
| `timeout` | `10s` | How long a search, list, get or delete call may take. Must not be negative; 0 or unset uses the default. |
| `addTimeout` | `120s` | How long an add call may take. It is longer because mem0 runs model extraction inline. Must not be negative; 0 or unset uses the default. |
| `recallTimeout` | `2s` | How long the search before an agent's model call may take. A slow mem0 delays the agent's reply by at most this long. Must not be negative; 0 or unset uses the default. |
| `apiKeySecret.namespace` | none | The namespace of mem0's admin key Secret, which is the namespace the mem0 pack is installed in. Required when memory is on. |
| `apiKeySecret.name` | `mem0-admin-key` | The Secret's name, a valid Kubernetes Secret name. This is the pack's default; change it if you set `adminKey.secretName` or `adminKey.existingSecret` in the pack. |
| `apiKeySecret.key` | `api-key` | The key inside the Secret that holds the admin key, a valid Secret data key. The key inside the Secret the mem0 pack makes is always `api-key` (the pack's chart fixes it), so change this only for a Secret you manage yourself. |

The chart passes this section into the config file as written and creates no Role in mem0's namespace. The gateway reads the key through the Role the mem0 pack creates for the accounts in `callers`.

The gateway refuses to start with memory on when `url` is missing, is not an `http` or `https` address, or carries a user name, password, query or fragment, when `timeout`, `addTimeout` or `recallTimeout` is negative, when `apiKeySecret.namespace` is missing or is not a valid namespace name, when `apiKeySecret.name` or `apiKeySecret.key` is not valid, or when `executor` is not `kubernetes`.

## What the gateway logs

The gateway never logs memory text, what people typed, answers, recall queries, Social Security numbers or the key. Each line carries the person (`user`) where one is involved.

At start:

- Memory off: nothing about memory is logged.
- `memory.ready` with `url`: the gateway asked mem0 for the memories of a reserved name (`tapas-start-check`, which belongs to no person) and mem0 answered. It is logged once.
- `memory.unreachable` with `url` and `err`: mem0 did not answer correctly. The gateway checks in the background and keeps retrying, after 5 seconds and then at doubling waits up to one minute, until mem0 answers; it logs `memory.unreachable` again only when the `err` text changes, and `memory.ready` once when mem0 answers. The `err` text says why. `mem0 refused the key` means mem0 answered 401, so the key in the Secret is not the one mem0 uses (the Secret was replaced, or `apiKeySecret` names the wrong one); the key is read only at start, so restart the gateway after fixing it. `GET /memories answered 404` means `url` does not point at the mem0 pack's Service: the pack answers 404 for routes it does not serve, so check the address and port. A message such as `GET /memories answered 503`, or a connection error, means mem0 is down, still starting or not reachable from the gateway, for example because the `callers` entry does not match the gateway pod. The gateway keeps running while it waits.
- The start fails with `read the mem0 key Secret <namespace>/<name>` when the Secret is missing or the gateway may not read it (the `callers` entry is wrong). It fails with `Secret <namespace>/<name> has no "<key>"` when the Secret has no such key or its value is empty. It fails when the value has leading or trailing whitespace, for example a trailing newline from `echo` or a file: mem0 compares the key exactly, so a padded value could never work, and Tapas refuses it rather than trim it. Store the key with no whitespace around it (`printf %s`, or `kubectl create secret ... --from-literal`). The error never contains the key.

While agents run:

| Line | Level | Meaning |
|---|---|---|
| `memory.recall_failed` (`err`) | warn | The search before a model call failed or took longer than `recallTimeout`. The call went on without memories, and the failure is not cached. |
| `memory.learned` (`count`) | info | mem0 stored `count` memories from a turn. `count` is 0 when it found no lasting fact. |
| `memory.learn_failed` (`err`) | warn | mem0 could not be reached or refused the add. Nothing was stored for that turn. |
| `memory.learn_skipped` (`reason=busy`) | info | Four learn calls were already running on this gateway, so this turn was not learned from. |
| `memory.learn_skipped` (`reason=too_long`) | info | A "remember that" sentence was longer than 1000 characters and was not stored. |
| `memory.write_refused` (`reason=ssn`) | info | The text held a Social Security number, so nothing was sent to mem0. |
| `memory.learn_panicked` (`stack`) | error | A bug in learning was caught. The person's request was already answered and the gateway kept running. Report it with the stack. |
| `memory.list_failed`, `memory.delete_failed` (`err`) | warn | The Memories page could not reach mem0. The person saw the 502 message above. |

## Limits

- Memory needs the Kubernetes executor. With the Docker or process executor the gateway refuses to start when `memory` is set.
- Memories are per person only. There is no team or shared memory, and one person's memories are never recalled for another.
- The recall cache is kept in each gateway replica's memory. A memory deleted on one replica can still be recalled from another replica's cache for up to 2 minutes.
- mem0 keeps every exchange it was sent in its own history database, even when it extracts no memory from it. Deleting a memory on the Memories page removes the memory, not that history.
- pi's summarization calls and very long pastes (over 8000 characters) are skipped: nothing is recalled for them or learned from them.
- The key is read once, at start. After the key changes (a new Secret, or a rotation in mem0), restart the gateway so it reads the new one.
- The gateway needs mem0's key Secret to exist when it starts, and fails to start until it does. Install the mem0 pack first, or expect the gateway pod to restart until the Secret appears.
- Memories belong to the person's identity name, which is never given to anyone else. `javo user remove` leaves a person's memories in mem0, as it leaves their home files; if the same person comes back (`javo user add --reuse-person`), they get them again.
- mem0 has no per-person access control: anything holding its admin key can read every person's memories. Tapas scopes each call to one person and keeps the key to the gateway, and the pack's NetworkPolicy lets only the pods in `callers` connect. Do not add other callers or share the Secret.
