# Memory client Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Tapas can be configured to use the mem0 pack, reads mem0's admin key from mem0's namespace at start (tapas#15), and has a mem0 client and an owner-checking memory service, wired into the gateway with a start check.

**Architecture:** A `memory` config section (Kubernetes executor only) names mem0's address and key Secret. The gateway's cluster start path reads the key with its cache-less client, using RBAC the mem0 pack grants. `internal/memory` holds `Mem0` (the HTTP client for the pack's allowlisted REST subset) and `Service` (every call for one owner; foreign memories are not found). Wiring builds the service; the gateway probes mem0 once at start and logs the result.

**Tech Stack:** Go (module `github.com/nebari-dev/javo`), `net/http`, `encoding/json`, controller-runtime client and fake client, `httptest`, Helm chart tests in `test/helm`, Astro docs site, Docker for the integration script.

**Spec:** `docs/superpowers/specs/2026-11-25-memory-client-design.md`

## Journeys

Defined by the builder under the owner's delegation (2026-11-25).

| # | Item | Proof | Check method | Evidence |
|---|------|-------|--------------|----------|
| 1 | An admin turns memory on and the gateway reads mem0's key from mem0's namespace at start | With `memory.enabled` and `apiKeySecret.namespace: mem0`, `memoryKeyOf` on a cluster holding Secret `mem0/mem0-admin-key` returns its trimmed `api-key`; the same call with the Secret absent, the key absent, or an empty value returns an error naming `mem0/mem0-admin-key` and not containing the value | automated: `go test ./internal/cli/servercmd -run Memory` || 2026-11-25 @9eca44a: `go test -v ./internal/cli/servercmd -run Memory` PASS TestMemoryKeyIsReadFromMem0sNamespace, TestNoMemoryMeansNoSecretRead; ok |
| 2 | A memory misconfiguration fails at load and says what is wrong | `memory.enabled` without `url`, with a non-http URL, without `apiKeySecret.namespace`, or under executor docker or process, each fails `config.Parse` with an error naming the key; an unset section loads with memory off | automated: `go test ./internal/config -run Memory` || 2026-11-25 @21cdbff: `go test -v ./internal/config -run Memory` PASS TestLoadReadsMemory, TestLoadKeepsExplicitMemoryValues, TestMemoryIsOffByDefault, TestLoadRejectsBadMemoryConfig (now also refuses userinfo, query and fragment, and no error echoes the URL); ok |
| 3 | Tapas can add, search, list, get and delete one person's memories in the real mem0 pack | Against the mem0 pack server image (allowlist on) with pgvector and the fake model in Docker: an add for alice, then search and list for alice find it, get by id returns it with alice's id, delete removes it, and a get afterwards is `ErrNotFound` | automated: `bash scripts/test/mem0-integration.sh` (not in CI; Docker and a mem0 pack checkout at a pinned commit); narrated: its transcript || 2026-11-25 @9eca44a: `MEM0_PACK_DIR=<mem0-pack at 54e57b9> bash scripts/test/mem0-integration.sh`: migrations applied, healthz 200, `ok github.com/nebari-dev/javo/internal/memory` |
| 4 | One person can never read or delete another person's memory through the service | With memories for alice and bob seeded in `memorytest.Fake`: `Get(alice, bobsID)` and `Delete(alice, bobsID)` return `ErrNotFound` and the fake records no delete call; `Search`/`List` for alice never return bob's memory even when the client returns it; an empty owner is refused before any client call | automated: `go test ./internal/memory -run Service` || 2026-11-25 @9eca44a: `go test -v ./internal/memory -run Service` PASS TestServiceGetAndDeleteActOnlyForTheOwner, TestServiceDropsResultsThatAreNotTheOwners, TestServiceRefusesWithoutAPersonBeforeCallingTheStore, TestServiceAddRunsTheWriteCheckFirst |
| 5 | mem0's key never appears in logs or errors | Client tests drive every error path (401, 404, 500, oversized body, transport failure) and assert no error string contains the key; key-read tests assert the same; the probe's log line carries no key | automated: `go test ./internal/memory ./internal/cli/servercmd` || 2026-11-25 @9eca44a: `go test ./internal/memory ./internal/cli/servercmd` ok, ok |
| 6 | The gateway says at start whether mem0 answers with the key | The probe against a fake mem0 that accepts the key logs `memory.ready`; one that answers 401 logs `memory.unreachable` with the status; the gateway keeps running in both cases | automated: `go test ./internal/memory -run Probe` || 2026-11-25 @9eca44a: `go test -v ./internal/memory -run Probe` PASS TestProbeLogsWhetherMem0Answers (ready and 401 cases, log has no key) |
| 7 | Turning memory off changes nothing | With no `memory` section the gateway reads no Secret (the fake client records no Get) and `wiring.Memory` returns nil; existing tests pass unchanged | automated: `go test ./internal/cli/... ./internal/config` and the full `make test` || 2026-11-25 @9eca44a: `go test ./internal/cli/... ./internal/config` 14 ok, 0 FAIL; TestMemory (wiring) PASS; `make test-local` no failures. Full `make test`: local failures only in internal/files, hosthome and imagetag, which fail identically on origin/main here; CI is the check (row 9) |
| 8 | An admin can set it up from the docs | `site/src/content/docs/admin/memory.md` covers installing the mem0 pack with Tapas as caller (ServiceAccount by engine, pod labels, namespace), the config section, the start log lines and the limitations; the site builds | narrated: page headings and the site build output || 2026-11-25 @9eca44a: page has sections What memory does today, Install the mem0 pack with Tapas as its caller, Turn it on in Tapas, What the gateway logs at start, Limits; `pnpm@10.34.7 exec astro build`: 40 page(s) built, /admin/memory/index.html |
| 9 | The PR proves itself in CI | Go build, vet, local build, tests (SQLite and Postgres shards) and govulncheck pass on the PR | narrated: `gh pr checks` transcript || pending: Slice 1 (#16) checks green; Slice 2 (#17) and Slice 3 CI not yet recorded |

## Global Constraints

- No new Go dependency. Tests first; behaviour-sentence test names; table-driven where there are several cases; plain-English comments; error texts never contain key material; slog key/value; errors `fmt.Errorf("verb noun: %w", err)`; `memory` package errors prefixed `mem0: ` for client failures.
- `make test-local` must keep passing: cluster-only code in `*_cluster.go`; memory is refused for docker and process executors at config load.
- No RBAC change in the Tapas chart and no `+kubebuilder:rbac` marker for the mem0 Secret.
- Defaults: `memory.timeout` 10s, `memory.addTimeout` 120s, `memory.apiKeySecret.name` `mem0-admin-key`, `memory.apiKeySecret.key` `api-key`; `memory.apiKeySecret.namespace` required when enabled.
- mem0 contract: header `X-API-Key`; owner (`user_id`) required, never empty or `*`; adds carry scope in top-level fields, never `filters`; search scope in `filters`; list `?user_id=&top_k=1000`; ids are lowercase UUIDs; `GET /memories/{id}` `200 null` means not found.
- Probe person: `tapas-start-check`; log events `memory.ready` and `memory.unreachable`.
- Commit prefixes as in history (`feat:`, `fix:`, `docs:`, `test:`); no em dashes; no AI attribution.
- Each slice is one PR on `nebari-dev/tapas`, stacked on the previous slice's branch. Do not merge.

## Review Focus

1. A mem0 response whose memory has another `user_id`, no `user_id`, or a `user_id` differing only in case: the service must treat it as not the owner's (Task 4 tests).
2. An owner of `"*"`, `""`, or whitespace: refused with `ErrInvalid` before any HTTP request (Task 3 tests).
3. The key Secret holds the key with trailing newline, or the `data` map has the value under a different key: trimmed in the first case, an error naming the Secret and key in the second (Task 2 tests).
4. mem0 answers with an HTML error page or a body larger than the limit: an error with method, path and status only (Task 3 tests).
5. Memory enabled but mem0 not installed yet: the gateway fails start with a message naming the Secret, and starts once it exists (Task 2 test plus docs).

---

## Slice Map
| # | Slice | Repos | PRs | Depends on | Journeys |
|---|---|---|---|---|---|
| 1 | Memory config and the cross-namespace key read | tapas | tapas#16 | - | 1, 2, 5, 7 |
| 2 | mem0 client and owner-checking service | tapas | tapas#17 | Slice 1 | 3, 4, 5 |
| 3 | Wiring, start check and admin docs | tapas | tapas (Slice 3 PR, pending) | Slice 2 | 6, 7, 8, 9 |

Merge order: #16 -> #17 -> Slice 3.

## Slice 1: Memory config and the cross-namespace key read
**Goal:** An admin can turn memory on in config, misconfiguration fails at load, and the gateway reads mem0's key from mem0's namespace at start (tapas#15).
**Repos / branches:** tapas: `feat/memory-client` (base: main)
**PRs:** tapas#A "Read the mem0 key from mem0's namespace"
**Depends on:** -
**Contracts touched:** Config section memory
**Docs updated:** none
**Advances journeys:** 1, 2, 5, 7
**Validation:**
| Step | Command / action | Expected |
|---|---|---|
| config | `go test -count=1 ./internal/config/...` | ok |
| servercmd | `go test -count=1 ./internal/cli/...` | ok |
| local build | `make test-local` | ok |
| vet | `go vet ./...` | clean |
| chart | `go test -count=1 ./test/helm/...` | ok |

**Evidence (2026-11-25, head 11f4d38):** `go test ./internal/config/...` 1 ok; `go test ./internal/cli/...` 12 ok; `go test ./test/helm/...` ok (RBAC markers unchanged); `go vet ./...` clean; `make test-local` no failures.

### Task 1: The memory config section

**Files:**
- Modify: `internal/config/config.go` (`Platform.Memory`, `MemoryCfg`, `SecretKeyRef`, defaults, validation), `internal/config/executor.go` (`misplaced`), `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.MemoryCfg{Enabled bool; URL string; Timeout, AddTimeout metav1.Duration; APIKeySecret SecretKeyRef}`, `config.SecretKeyRef{Namespace, Name, Key string}`, `Platform.Memory` with json key `memory`.

- [ ] **Step 1: Failing tests** in `config_test.go`:
  - `TestLoadReadsMemory`: a config with `memory: {enabled: true, url: http://mem0-nebari-mem0.mem0.svc:8000, apiKeySecret: {namespace: mem0}}` under executor kubernetes loads with timeout 10s, addTimeout 120s, name `mem0-admin-key`, key `api-key`; explicit values override defaults.
  - Rows in `TestLoadRejectsBadConfig` (or a table test `TestLoadRejectsBadMemoryConfig`): enabled without url (`memory.url`), url `ftp://x` (`memory.url`), url without host (`memory.url`), enabled without namespace (`memory.apiKeySecret.namespace`), namespace `Mem0_NS` (`memory.apiKeySecret.namespace`), memory section under executor docker and under process (error names `memory`).
  - `TestMemoryIsOffByDefault`: a config with no memory section loads with `Memory.Enabled == false`.
- [ ] **Step 2:** `go test ./internal/config -run Memory`. Expected: FAIL.
- [ ] **Step 3:** Implement following the Nebi section's pattern (`config.go:912-922`) and `misplaced`'s `only(...)`. Validate the namespace with `k8s.io/apimachinery/pkg/util/validation.IsDNS1123Label` (already a dependency).
- [ ] **Step 4:** Re-run the config tests. Expected: PASS.
- [ ] **Step 5:** Commit `feat(config): a memory section for mem0`.

### Task 2: Read the mem0 key at gateway start

**Files:**
- Modify: `internal/cli/servercmd/gateway_cluster.go` (call `memoryKeyOf` in `clusterParts` when memory is on), `internal/cli/servercmd/host.go` (`gatewayParts.memoryKey string`)
- Create: `internal/cli/servercmd/memory_cluster.go` (`memoryKeyOf`), `internal/cli/servercmd/memory_cluster_test.go`

**Interfaces:**
- Consumes: `config.MemoryCfg` (Task 1).
- Produces: `func memoryKeyOf(ctx context.Context, c client.Client, m config.MemoryCfg) (string, error)`; `gatewayParts.memoryKey` (empty when memory is off).

- [ ] **Step 1: Failing table test** `TestMemoryKeyIsReadFromMem0sNamespace` with a controller-runtime fake client:
  - Secret `mem0/mem0-admin-key` with `api-key: "abc123\n"` gives `abc123`.
  - Secret absent: error contains `mem0/mem0-admin-key`.
  - Secret present without key `api-key`, and with an empty value: error contains `mem0/mem0-admin-key` and `api-key`.
  - Every error string does not contain the value used in the case.
  - A Secret of the same name in the release namespace only (not mem0's) is not used: error.
  And `TestNoMemoryMeansNoSecretRead`: with memory off, the code path that builds gateway parts performs no Get on the client (wrap the fake client to count Gets, or test the helper that decides).
- [ ] **Step 2:** `go test ./internal/cli/servercmd -run Memory`. Expected: FAIL.
- [ ] **Step 3:** Implement following `backupPassword` in `common_cluster.go:29-35`: `client.Get` the Secret by namespace and name, `strings.TrimSpace(string(sec.Data[key]))`, errors `"read the mem0 key Secret %s/%s: %w"` and `"Secret %s/%s has no %q"`. Call it from `clusterParts` only when `platform.Memory.Enabled`; store in `gatewayParts.memoryKey`. Do not log the key.
- [ ] **Step 4:** Re-run; then `go test ./internal/cli/...`, `make test-local`, `go vet ./...`. Expected: PASS.
- [ ] **Step 5:** Commit `feat(gateway): read mem0's key from mem0's namespace at start`.

**Checkpoint (controller, after the last task; implementers ignore):** run validation, paste evidence, open PR(s), STOP for go-ahead.

## Slice 2: mem0 client and owner-checking service
**Goal:** `internal/memory` can add, search, list, get and delete one person's memories in the mem0 pack and never returns or touches another person's.
**Repos / branches:** tapas: `slice/memory-2-client` (base: `feat/memory-client`)
**PRs:** tapas#B "A mem0 client and an owner-checking memory service"
**Depends on:** Slice 1
**Contracts touched:** memory.Client and memory.Service
**Docs updated:** none
**Advances journeys:** 3, 4, 5
**Validation:**
| Step | Command / action | Expected |
|---|---|---|
| unit | `go test -count=1 -race ./internal/memory/...` | ok |
| vet | `go vet ./...` and `go vet -tags mem0 ./internal/memory/` | clean |
| integration | `bash scripts/test/mem0-integration.sh` | ends with `ok  github.com/nebari-dev/javo/internal/memory` |

**Evidence (2026-11-25, head 1c8fa13):** `go test -race ./internal/memory/...` ok; `go vet ./...` and `go vet -tags mem0 ./internal/memory/` clean; `MEM0_PACK_DIR=... bash scripts/test/mem0-integration.sh` (mem0 pack at 54e57b9) ended `ok  github.com/nebari-dev/javo/internal/memory`.

### Task 3: The mem0 client

**Files:**
- Create: `internal/memory/memory.go` (types, errors, `Client`), `internal/memory/mem0.go`, `internal/memory/mem0_test.go`

**Interfaces:**
- Produces: `memory.Message{Role, Text string}`, `memory.Memory{ID, Text, UserID, AgentID string; Score float64; CreatedAt, UpdatedAt time.Time}`, `ErrNotFound`, `ErrRefused`, `ErrInvalid`, `Client` (spec signatures), `NewMem0(baseURL, apiKey string, hc *http.Client) *Mem0` plus `(*Mem0).SetAddClient(hc *http.Client)` or an options struct carrying the add timeout client (implementer's choice, stated in the report).

- [ ] **Step 1: Failing tests** against an `httptest` fake mem0 (Go 1.22 method patterns, modelled on mem0 v2.2.1 `server/main.py` responses and the mem0 pack allowlist; cite both in a comment):
  - every request carries `X-API-Key` and no other auth header;
  - Add sends `{"messages":[{"role","content"}],"user_id":"alice","agent_id":"helper"}`, never `filters`, and returns the memories from `{"results":[{"id","memory","event"}]}`; empty `results` is success with no memories;
  - Search sends `{"query","filters":{"user_id":"alice","agent_id":"helper"},"top_k":5}` and omits `agent_id` when empty; maps `memory`, `user_id`, `agent_id`, `score`, ISO timestamps;
  - List requests `GET /memories?user_id=alice&top_k=1000`;
  - Get maps a memory; `200 null` is `ErrNotFound`; a non-lowercase-UUID id is `ErrNotFound` with no request made;
  - Delete sends `DELETE /memories/<uuid>`; a bad id makes no request;
  - owner `""`, `"*"`, `"  "` and Search `limit` 0 return `ErrInvalid` with no request;
  - status mapping table: 401 `ErrRefused`, 404 `ErrNotFound`, 400/500 errors containing method, path and status; an oversized body errors;
  - no error string in any case contains the API key.
- [ ] **Step 2:** `go test ./internal/memory -run Mem0`. Expected: FAIL.
- [ ] **Step 3:** Implement following `internal/features/envnebi/nebi.go` (one `do`, size-limited body, sentinel mapping, path-only error texts). Use the add client for Add, the default client otherwise.
- [ ] **Step 4:** Re-run with `-race`. Expected: PASS.
- [ ] **Step 5:** Commit `feat(memory): a mem0 client for the mem0 pack's endpoints`.

### Task 4: The owner-checking service and its fake

**Files:**
- Create: `internal/memory/service.go`, `internal/memory/service_test.go`, `internal/memory/memorytest/fake.go`

**Interfaces:**
- Consumes: `Client`, types (Task 3).
- Produces: `type WriteCheck func(ctx context.Context, owner, agent string, msgs []Message) error`; `NewService(c Client, check WriteCheck) *Service` with the spec's methods; `memorytest.New(seed ...memory.Memory) *Fake` implementing `memory.Client`, recording calls (`Calls() []Call` with method and arguments), assigning lowercase UUIDs to added memories.

- [ ] **Step 1: Failing table tests** (`service_test.go`) using `memorytest`:
  - `Get(alice, bobsID)` and `Delete(alice, bobsID)` return `ErrNotFound`; the fake recorded no Delete;
  - `Get(alice, alicesID)` returns it; `Delete(alice, alicesID)` deletes it;
  - `Search`/`List` drop results whose `UserID` is not exactly the owner (a fake that returns bob's memory for alice), including `""` and `"Alice"`;
  - empty owner on every method is `ErrInvalid` with no client call;
  - `Add` with a check returning an error adds nothing and returns that error; with nil check it adds.
- [ ] **Step 2:** Run. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Re-run with `-race`. Expected: PASS.
- [ ] **Step 5:** Commit `feat(memory): a service that only acts for the memory's owner`.

### Task 5: Integration test against the real mem0 pack

**Files:**
- Create: `internal/memory/mem0_integration_test.go` (`//go:build mem0`), `scripts/test/mem0-integration.sh`

**Interfaces:**
- Consumes: `NewMem0`, `Service` (Tasks 3-4).

- [ ] **Step 1:** Write the test: reads `MEM0_URL` and `MEM0_API_KEY` (skips if unset), then for owner `alice-<random>` and agent `helper`: Add a two-message conversation; Search and List for that owner find a memory; Get by id returns it with the owner's id; `Service.Get(otherOwner, id)` is `ErrNotFound`; Delete removes it; Get afterwards is `ErrNotFound`.
- [ ] **Step 2:** Write the script (`set -euo pipefail`, unique names, EXIT trap cleanup): shallow-clone `https://github.com/nebari-dev/mem0-pack` at a pinned commit (`MEM0_PACK_REF`, default the commit the slice stack's `slice/10-docs` points to when this task runs; record it in the script) into a temp dir; build `image/` and `tests/e2e/fake-model/`; start pgvector (the pack's pinned digest), create the non-superuser `mem0` role and database and `CREATE EXTENSION vector` as the pack's `image/smoke.sh` does; run the pack's migrate entrypoint then the server with the same env as `smoke.sh` (fake model with `REQUIRE_TOKEN=false`, 384 dims), publishing on 127.0.0.1 only; wait for `/healthz`; run `MEM0_URL=... MEM0_API_KEY=... go test -tags mem0 -count=1 -run Integration ./internal/memory/`.
- [ ] **Step 3:** Run the script. Expected: `ok  github.com/nebari-dev/javo/internal/memory`.
- [ ] **Step 4:** Commit `test(memory): run the client against the real mem0 pack`.

**Checkpoint (controller, after the last task; implementers ignore):** run validation, paste evidence, open PR(s), STOP for go-ahead.

## Slice 3: Wiring, start check and admin docs
**Goal:** With memory on, the gateway builds the memory service and logs at start whether mem0 answers; an admin can set it up from the docs.
**Repos / branches:** tapas: `slice/memory-3-wiring` (base: `slice/memory-2-client`)
**PRs:** tapas#C "Wire the memory service and document the setup"
**Depends on:** Slice 2
**Contracts touched:** Gateway log lines memory.ready and memory.unreachable
**Docs updated:** none
**Advances journeys:** 6, 7, 8, 9
**Validation:**
| Step | Command / action | Expected |
|---|---|---|
| tests | `go test -count=1 ./internal/memory/... ./internal/cli/... ./test/helm/...` | ok |
| all | `make test` | ok |
| local | `make test-local` | ok |
| docs | `make docs-build` or the site build command in `site/` | builds |
| CI | `gh pr checks <PR>` | Go build, vet, local, tests, govulncheck pass |

**Evidence (2026-11-25, head 8a3bdd2):** `go test -count=1 ./internal/memory/... ./internal/cli/... ./test/helm/...` all ok; `make test-local` no failures; `go vet ./...` clean; docs built with `npx pnpm@10.34.7 install --frozen-lockfile && pnpm exec astro build` (40 pages, `/admin/memory/index.html` among them). `make test` fails locally in `internal/files` (TestDecodeListingByteCap), `internal/storage/hosthome` (/root is a symlink on this host) and `test/imagetag` (`shasum` not installed); the same three fail on origin/main in this environment and this branch touches none of those packages, so CI is the check for them.

### Task 6: Wiring and the start check

**Files:**
- Create: `internal/memory/probe.go`, `internal/memory/probe_test.go`
- Modify: `internal/cli/wiring/wiring.go`, `internal/cli/wiring/wiring_test.go`, `internal/cli/servercmd/gateway.go`

**Interfaces:**
- Consumes: `NewMem0`, `NewService` (Slice 2), `gatewayParts.memoryKey` (Slice 1).
- Produces: `func Probe(ctx context.Context, s *Service, url string) error` (logs `memory.ready` with `url` or `memory.unreachable` with `url` and `err`, returns the error); `func Memory(c config.MemoryCfg, key string) *memory.Service` in wiring.

- [ ] **Step 1: Failing tests:** `TestProbeLogsReady` and `TestProbeLogsUnreachable` (capture slog with a test handler; 200 empty list vs 401; the log has no key); `TestMemoryIsNilWhenOff` and `TestMemoryBuildsAServiceWhenOn` in wiring.
- [ ] **Step 2:** Run. Expected: FAIL.
- [ ] **Step 3:** Implement. In `gateway.go` run: `if svc := wiring.Memory(platform.Memory, parts.memoryKey); svc != nil { go memory.Probe(ctx, svc, platform.Memory.URL) }` with a comment that recall, learning and the API take the service from here.
- [ ] **Step 4:** `go test ./internal/memory/... ./internal/cli/...`, `make test-local`, `go vet ./...`. Expected: PASS.
- [ ] **Step 5:** Commit `feat: build the memory service and check mem0 at gateway start`.

### Task 7: Chart example, chart test and admin docs

**Files:**
- Modify: `deploy/helm/javo/values.yaml` (commented `memory:` example under `config:`), a `test/helm` test file
- Create: `site/src/content/docs/admin/memory.md`; add it to the site's sidebar if the sidebar is listed explicitly

- [ ] **Step 1:** Failing chart test `TestMemoryConfigReachesTheConfigFile`: render with `config.memory` set (enabled, url, apiKeySecret.namespace) and assert the rendered config file contains the same section, and that no Role or RoleBinding outside the release and workspace namespaces is rendered.
- [ ] **Step 2:** Run. Expected: FAIL only if the passthrough drops it; if it passes immediately, keep it as a guard and say so in the report.
- [ ] **Step 3:** Write the values example and `admin/memory.md`: what memory does today (setup only, recall and learning to follow); install the mem0 pack from a checkout of nebari-dev/mem0-pack (no chart published until mem0-pack#12) with Tapas's gateway as its caller: namespace of the Tapas release, ServiceAccount `javo-controller` with SQLite or `javo-gateway` with Postgres, and the gateway pod's labels as this chart renders them for each engine (read them from the templates and verify with `helm template`); the `memory` config section with defaults; the start log lines; limitations (Kubernetes only, key read at start, rotation needs a restart, Tapas fails start until mem0's key Secret exists). Plain English, no em dashes.
- [ ] **Step 4:** Run the chart test and build the docs site. Expected: PASS, site builds.
- [ ] **Step 5:** Commit `docs: set up memory with the mem0 pack`.

**Checkpoint (controller, after the last task; implementers ignore):** run validation, paste evidence, open PR(s), STOP for go-ahead.
