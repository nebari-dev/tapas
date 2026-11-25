# Memory client: design

**Date:** 2026-11-25
**Status:** approved for build (scope chosen by the owner on 2026-11-25; the rest delegated to the builder and marked "builder's call")
**Repos:** `nebari-dev/tapas` (this repo). Depends on the mem0 pack's contracts (`nebari-dev/mem0-pack`, `docs/architecture/contracts.md`).
**Issues:** nebari-dev/tapas#15 (read the mem0 key from mem0's namespace), plus the memory client from the policy and memory design.
**Parent design:** "Policy and memory proxy for agents" (`docs/superpowers/specs/2026-11-24-policy-memory-proxy-design.md`, Memory section and Program Design). That file is a draft not yet in this repo; this spec repeats what it needs from it.

## Intent

Tapas will give each person memory that follows them across agents and models, stored in mem0 run by the mem0 pack. mem0 has no per-person access control: whoever holds its admin key can read or delete anyone's memories. The parent design makes Tapas the only key holder and the place where every memory call is scoped to one person and checked for ownership.

This change builds the part of that design that everything else stands on: Tapas can be configured to use mem0, reads mem0's key from mem0's own namespace when it starts, and has a client and a service that only ever act for one named person and refuse a memory that belongs to someone else. Recall into model prompts, learning from answers, the `/api/memories` endpoints and the policy check on memory writes are later work that builds on this.

## Goals

- A `memory` config section, off by default, that names mem0's address and where its key lives.
- With memory on, the gateway reads mem0's admin key at start from a Secret in mem0's namespace through the Kubernetes API, using the RBAC grant the mem0 pack makes (tapas#15). No hand-made copy of the key in Tapas's namespace.
- `internal/memory`: a mem0 client covering the endpoints the mem0 pack admits (add, search, list, get, delete), and a service that requires a person on every call and treats another person's memory as not found.
- The gateway checks at start that mem0 answers with the key, and says so in its log.
- An admin can set it up from the docs.

## Non-goals

- Recall (adding memories to model requests) and learning (sending exchanges to mem0). They change the model proxy and need the global and project memory question below settled.
- `GET/DELETE /api/memories` and any UI.
- The `memory_write` policy checkpoint: the policy engine does not exist yet. The service leaves a seam for it.
- Docker and process executors. The mem0 pack runs on Kubernetes, and only the Kubernetes executor has a Secret API.
- Rotating the key without a restart. Like the sessions key, it is read once at start.

## Anti-goals (a failure even if it works)

- The key in any log line, error message, API response or file.
- A memory read, update or delete on behalf of one person returning or touching another person's memory.
- A call to mem0 with no person, or with a wildcard person.
- Turning memory on silently doing nothing: a misconfiguration must fail at config load or at start, with a message that names what is wrong.
- Any RBAC added to the Tapas chart for this: the grant belongs to the mem0 pack, which owns the Secret.

## Constraints

- Tapas conventions (`CONTRIBUTING.md`, `CLAUDE.md`): tests first; behaviour-sentence test names; table-driven where there are several cases; no new dependency (`net/http`, `encoding/json`, the existing controller-runtime client); plain-English comments; error texts never carry key material; commit prefixes as in history.
- The `local` build tag must still build: cluster-only code lives in `*_cluster.go` with a stand-in in `cluster_local.go`.
- `internal/authz` remains the one place that decides what a person may do to an app; memory ownership is a separate check in `internal/memory`.
- The parent design reserves `internal/cli/wiring/wiring.go` for Lane B (Amit). The owner chose to build this here; the wiring change is a single new constructor and its call, and the PR description says so.
- mem0 contract (mem0 pack, `docs/architecture/contracts.md`): header `X-API-Key`; every add, list and search names a literal, non-empty `user_id` (no `*`); `agent_id` and `run_id` only narrow; adds carry the scope in top-level fields and never `filters`; `GET /memories` takes only `user_id`, `agent_id`, `run_id`, `top_k`, `show_expired`; ids are lowercase UUIDs; `GET /memories/{id}` answers `200 null` for an id that does not exist; mem0 does not check ownership on by-id calls.

## Approaches

Decided with the owner:

| Question | Options | Decision |
|---|---|---|
| Scope | A: tapas#15 only (flag and key read); B: tapas#15 plus the memory client and its wiring | **B** |

Builder's calls (delegated):

| Question | Options considered | Call and reason |
|---|---|---|
| Where the key's location is configured | Gateway flags like `--sessions-key-secret`; the `memory` config section | **Config section** (`memory.apiKeySecret`). The address and the key location belong together, and the chart passes `config` through to the file unchanged, so no chart template changes. Flags exist for cluster plumbing the chart controls; this is an admin setting. |
| Where the key is read | `commonFlags.load` (like the backup password); the gateway's `clusterParts` | **`clusterParts`, gateway only.** With Postgres the controller runs separately under its own ServiceAccount, which the mem0 pack does not grant; reading in common load would make the controller fail. |
| Key missing or unreadable at start | Fail start; log and run with memory off | **Fail start** with a message naming the Secret (not its value). Matches the sessions key and backup password, and the anti-goal against silently doing nothing. When Tapas syncs before mem0, the gateway restarts until mem0's key exists. |
| mem0 unreachable at start (key read fine) | Fail start; warn and continue | **Warn and continue.** mem0 being briefly down must not take Tapas down; the probe's log line makes a wrong key or address visible. |
| Executors | Kubernetes only; a key file for docker/process | **Kubernetes only**, refused at config load for other executors through `misplaced()`. |
| Client shape | Spec's `Client` interface with `policy.Message`; local types | **The spec's interface with a local `memory.Message{Role, Text}`**, the same fields as the planned `policy.Message`, because `internal/policy` does not exist yet. |
| Memory write check | Invent a `Decider` now; leave a seam | **A seam:** `Service` takes an optional `WriteCheck func(ctx, owner, agent string, msgs []Message) error`; nil means allow. The policy engine fills it later without changing callers. |

## Design

### Config (`internal/config`)

```go
// MemoryCfg turns on mem0-backed memory. Off by default.
type MemoryCfg struct {
	Enabled      bool            `json:"enabled"`
	URL          string          `json:"url"`          // mem0 pack Service, e.g. http://mem0-nebari-mem0.mem0.svc:8000
	Timeout      metav1.Duration `json:"timeout"`      // search, list, get, delete; default 10s
	AddTimeout   metav1.Duration `json:"addTimeout"`   // add (mem0 runs model extraction inline); default 120s
	APIKeySecret SecretKeyRef    `json:"apiKeySecret"` // where mem0's admin key lives
}
// SecretKeyRef names one key of a Secret in a namespace.
type SecretKeyRef struct {
	Namespace string `json:"namespace"` // required when memory is on
	Name      string `json:"name"`      // default mem0-admin-key
	Key       string `json:"key"`       // default api-key
}
```

`Platform.Memory MemoryCfg `json:"memory"``. Defaults in `applyDefaults`: timeout 10s, addTimeout 120s, name `mem0-admin-key`, key `api-key` (the mem0 pack's defaults). `Validate`: when enabled, `memory.url` must be an `http`/`https` URL with a host and no userinfo, query or fragment; `memory.apiKeySecret.namespace` must be set and a valid DNS-1123 label. `misplaced()`: `memory` only with executor kubernetes.

### Key read (`internal/cli/servercmd`, tapas#15)

In `clusterParts` (gateway only), when `platform.Memory.Enabled`: read Secret `apiKeySecret.namespace/name` with the existing cache-less controller-runtime client; take `data[key]`, `strings.TrimSpace`; empty or missing is an error `"Secret ns/name has no \"key\""`; a read error is wrapped `"read the mem0 key Secret ns/name: %w"`. The value goes into a new `gatewayParts.memoryKey`. Never logged, never in an error. The `local` stand-in is unaffected (memory is refused for non-Kubernetes executors at load).

No RBAC change in the Tapas chart and no `+kubebuilder:rbac` marker: the mem0 pack's chart creates a Role in mem0's namespace allowing `get` on that one Secret, bound to the ServiceAccount named in its `callers` values. The gateway's account is `javo-controller` with SQLite and `javo-gateway` with Postgres (`javo.account` in `_helpers.tpl`).

### `internal/memory`

```go
type Message struct{ Role, Text string }
type Memory struct {
	ID, Text, UserID, AgentID string
	Score                     float64 // set by Search
	CreatedAt, UpdatedAt      time.Time
}
var (
	ErrNotFound = errors.New("memory not found")
	ErrRefused  = errors.New("mem0 refused the key")
	ErrInvalid  = errors.New("invalid memory request") // empty or wildcard owner, limit <= 0
)
type Client interface {
	Search(ctx context.Context, owner, agent, query string, limit int) ([]Memory, error)
	Add(ctx context.Context, owner, agent string, msgs []Message) ([]Memory, error) // the memories mem0 stored; empty when nothing was extracted
	List(ctx context.Context, owner string) ([]Memory, error)
	Get(ctx context.Context, id string) (Memory, error)
	Delete(ctx context.Context, id string) error
}
```

`Mem0` (`NewMem0(baseURL, apiKey string, hc *http.Client) *Mem0`) implements `Client` against the mem0 pack's REST subset: `X-API-Key` on every request; owner required, never `*`, and never with leading or trailing whitespace (mem0 strips it, so `" alice"` would act as `alice`); the same rules for a non-empty agent (`ErrInvalid` before any request); redirects are refused so `X-API-Key` never leaves for another location; adds send top-level `user_id`/`agent_id` and use the add timeout; a timed-out add may still have been stored, so callers must not retry it blindly; search sends `filters` with `user_id` and, when set, `agent_id`, and `top_k` (`limit <= 0` is `ErrInvalid`); list sends `?user_id=` and `top_k=1000` (mem0 server's cap; more than 1000 memories for one person are not listed); get and delete refuse an id that is not a lowercase UUID with `ErrNotFound` before any request; `200 null` from get is `ErrNotFound`; 401 is `ErrRefused`; 404 is `ErrNotFound` (`Service.Delete` gets first, so a foreign or unknown id never reaches mem0's delete); other statuses are errors with method, path and status, never the key or the body; bodies read through a size limit. Errors are prefixed `mem0: `. Wire mapping (mem0 v2.2.1): responses wrap lists as `{"results": [...]}`; a memory's text is `memory`; `user_id` and `agent_id` are present only when set; `created_at`/`updated_at` are ISO 8601 strings; search results carry `score`.

`Service` (`NewService(c Client, check WriteCheck) *Service`) is what the rest of Tapas uses. Every method takes the owner (the Tapas person's name, the mem0 `user_id`) and refuses an empty one. `Get(ctx, owner, id)` returns `ErrNotFound` when the memory's `UserID` is not the owner. `Delete(ctx, owner, id)` gets first and deletes only the owner's memory, so a foreign id never reaches mem0's delete. `Add` runs `check` when set and adds only if it returns nil. `Search` and `List` pass through with the owner, and also drop any result whose `UserID` is not the owner, so a mem0 change cannot leak another person's memory.

`memorytest` holds an in-memory `Client` that records calls, for tests in later work.

### Wiring and the start check

`wiring.Memory(c config.MemoryCfg, key string) *memory.Service` returns nil when memory is off; otherwise a `Service` over `NewMem0(c.URL, key, &http.Client{Timeout: c.Timeout.Duration}, WithAddClient(&http.Client{Timeout: c.AddTimeout.Duration}))` with no write check. The gateway (`servercmd/gateway.go` run) builds it from `gatewayParts.memoryKey` and runs `memory.Probe(ctx, svc, c.URL)` once in the background: a list for a reserved person (`tapas-start-check`) that logs `slog.Info("memory.ready", "url", url)` on success or `slog.Warn("memory.unreachable", "url", url, "err", err)` otherwise. The service is not yet passed to any consumer; recall, learning and the API take it from here.

### Docs

`site/src/content/docs/admin/memory.md`: what memory is today (setup only), installing the mem0 pack from a checkout (no chart is published until nebari-dev/mem0-pack#12) with Tapas as its caller (ServiceAccount by engine, the gateway pod's labels by engine as rendered by this chart, namespace), the `memory` config section, what the gateway logs at start, and the limitations. `deploy/helm/javo/values.yaml` gains a commented `memory:` example under `config:`.

## Program Design
**Scale:** full

### Existing patterns
| Pattern | Tag | Source | Promote? |
|---|---|---|---|
| Optional features are a config section with `Enabled`, validated in `Validate` with `bad(key, ...)`, defaults in `applyDefaults` | [inferred] | internal/config/config.go:221-241, 869-922 | no |
| Settings that mean nothing on some executors are refused by `misplaced()` | [inferred] | internal/config/executor.go:79-111 | no |
| Cluster-only reads live in `*_cluster.go`, gateway inputs travel in `gatewayParts` | [inferred] | internal/cli/servercmd/gateway_cluster.go:43-97, host.go:125-151 | no |
| Secret values read at start: refuse empty, TrimSpace, never in errors | [inferred] | internal/cli/servercmd/common_cluster.go:29-35, internal/controller/secretskey/secretskey.go | no |
| Wiring is the only composition root; an off feature is a nil dependency | [documented: internal/cli/wiring/wiring.go:1-4] | internal/cli/wiring/wiring.go:142-285 | n/a |
| Hand-rolled HTTP clients: one `do`, status to sentinel errors, size-limited bodies, error texts carry the path, never a token | [inferred] | internal/features/envnebi/nebi.go:37-250 | no |
| Fakes for tests live in `<pkg>test` packages that implement the production interface and record calls | [inferred] | internal/ai/aitest/scripted.go | no |
| Tests named as behaviour sentences, table-driven where there are several cases | [documented: CONTRIBUTING.md "Conventions"] | internal/config/config_test.go:64-100 | n/a |

Promote? values set by the builder under the owner's delegation: Tapas has no `docs/architecture/` registry, and adding one belongs to Tapas's maintainers, so inferred rows are not promoted.

### Component contracts
| Contract | Status | Owner | Consumers | Verified by | Promote? | Notes |
|---|---|---|---|---|---|---|
| Config section memory | new | tapas | admins, the javo chart's config passthrough | n/a (new) | no | Keys and defaults in Design |
| memory.Client and memory.Service | new | tapas | gateway start check now; model proxy and API later | n/a (new) | no | |
| Gateway log lines memory.ready and memory.unreachable | new | tapas | operators | n/a (new) | no | |
| mem0 pack admin key Secret readable by named callers | unchanged | mem0-pack | tapas gateway | `gh api repos/nebari-dev/mem0-pack/contents/docs/architecture/contracts.md?ref=slice/10-docs` | no | External; Secret mem0-admin-key key api-key by default |
| mem0 REST subset behind the allowlist | unchanged | mem0-pack | memory.Mem0 | same as above | no | External |
| javo.account ServiceAccount names (javo-controller with SQLite, javo-gateway with Postgres) | unchanged (implicit) | tapas chart | mem0 pack callers values | `grep -n 'define "javo.account"' -A2 deploy/helm/javo/templates/_helpers.tpl` | no | Documented in admin/memory.md |

Contract rows use `Promote? = no` because Tapas keeps no contracts registry; each is documented in the admin page.

### Data models and types

See `MemoryCfg`, `SecretKeyRef`, `Message`, `Memory`, `Client` in Design. `type WriteCheck func(ctx context.Context, owner, agent string, msgs []Message) error`.

### Method signatures

```go
// internal/config
func (p Platform) Validate() error            // gains memory rules
func (p *Platform) applyDefaults()            // gains memory defaults

// internal/cli/servercmd (cluster build)
func memoryKeyOf(ctx context.Context, c client.Client, m config.MemoryCfg) (string, error)

// internal/memory
func NewMem0(baseURL, apiKey string, hc *http.Client) *Mem0
func (m *Mem0) Search(ctx context.Context, owner, agent, query string, limit int) ([]Memory, error)
func (m *Mem0) Add(ctx context.Context, owner, agent string, msgs []Message) ([]Memory, error)
func (m *Mem0) List(ctx context.Context, owner string) ([]Memory, error)
func (m *Mem0) Get(ctx context.Context, id string) (Memory, error)
func (m *Mem0) Delete(ctx context.Context, id string) error
func NewService(c Client, check WriteCheck) *Service
func (s *Service) Search(ctx context.Context, owner, agent, query string, limit int) ([]Memory, error)
func (s *Service) Add(ctx context.Context, owner, agent string, msgs []Message) ([]Memory, error)
func (s *Service) List(ctx context.Context, owner string) ([]Memory, error)
func (s *Service) Get(ctx context.Context, owner, id string) (Memory, error)
func (s *Service) Delete(ctx context.Context, owner, id string) error
func Probe(ctx context.Context, s *Service, url string) error

// internal/memory/memorytest
func New(seed ...memory.Memory) *Fake          // implements memory.Client, records calls

// internal/cli/wiring
func Memory(c config.MemoryCfg, key string) *memory.Service
```

### Program layout

```text
tapas/
~ internal/config/config.go, executor.go, config_test.go     memory section, defaults, validation, misplaced
~ internal/cli/servercmd/gateway_cluster.go, host.go, gateway.go   memoryKeyOf, gatewayParts.memoryKey, build + probe
+ internal/cli/servercmd/memory_cluster_test.go                    key read tests (fake client)
+ internal/memory/memory.go, mem0.go, service.go, probe.go          (follows internal/features/envnebi, internal/ai)
+ internal/memory/mem0_test.go, service_test.go, probe_test.go
+ internal/memory/memorytest/fake.go                                 (follows internal/ai/aitest)
+ internal/memory/mem0_integration_test.go                           build tag mem0: against the real mem0 pack image
+ scripts/test/mem0-integration.sh                                   clones the mem0 pack at a pinned commit, builds its server and fake-model images, runs pgvector, migrate and the server on loopback, then go test -tags mem0
~ internal/cli/wiring/wiring.go, wiring_test.go                      Memory constructor
~ deploy/helm/javo/values.yaml                                       commented memory example under config
~ test/helm/<config test>                                            a memory section renders into the config file
+ site/src/content/docs/admin/memory.md                              setup page
```

### Call stacks

```text
Production:
  javo gateway start (executor kubernetes, memory.enabled)
    → config.Load → Validate (memory rules) → misplaced (kubernetes only)
    → clusterParts → memoryKeyOf → client.Get Secret <ns>/<name> (gateway SA; Role made by the mem0 pack)
    → gatewayParts.memoryKey
    → wiring.Memory(cfg, key) → memory.NewService(memory.NewMem0(url, key, hc), nil)
    → go memory.Probe(ctx, svc) → Mem0.List("tapas-start-check") → GET /memories?user_id=tapas-start-check
      → slog memory.ready | memory.unreachable

Later (not in this change):
  model proxy recall/learn, /api/memories → memory.Service (owner from the signed-in person or agent owner)
```

### Component trees and DI

```text
servercmd gateway run
  key := parts.memoryKey                         (from clusterParts)
  svc := wiring.Memory(platform.Memory, key)    (nil when off)
  if svc != nil { go memory.Probe(ctx, svc) }
memory.Service ← memory.Client (Mem0 in production, memorytest.Fake in tests), WriteCheck (nil now)
```

### Test approach and seams
| Boundary | Seam | Real/fake | Level | Proves |
|---|---|---|---|---|
| Config | `config.Parse` | real | unit, table-driven | defaults, validation errors name the key, refusal on docker/process |
| Key read | controller-runtime `client.Client` | `fake.NewClientBuilder` with the Secret in another namespace | unit | value read and trimmed; missing Secret, missing key and empty value fail with the Secret's name and never the value |
| mem0 client | `*http.Client` against `httptest` | fake mem0 server modelled on the pack's allowlist and mem0 v2.2.1 responses | unit, table-driven | requests carry X-API-Key and the scope rules; status mapping; `200 null`; id check; no key in errors |
| Service | `memory.Client` | `memorytest.Fake` | unit | ownership on get/delete, foreign results dropped, empty owner refused, WriteCheck honoured |
| Probe | `*Service` over `httptest` | fake | unit | ready and unreachable log lines |
| Wiring | `wiring.Memory` | real | unit | nil when off, a service when on |
| Real mem0 | `Mem0` against the mem0 pack server image | real mem0 v2.2.1 + allowlist, fake model, pgvector in Docker | integration (build tag `mem0`) | add, search, list, get, delete work against the real server and its allowlist |
| Chart | `helm template` | real | test/helm | a `config.memory` section reaches the config file unchanged |

## Journeys

Defined by the builder under the owner's delegation (2026-11-25).

| # | Item | Proof | Check method | Evidence |
|---|------|-------|--------------|----------|
| 1 | An admin turns memory on and the gateway reads mem0's key from mem0's namespace at start | With `memory.enabled` and `apiKeySecret.namespace: mem0`, `memoryKeyOf` on a cluster holding Secret `mem0/mem0-admin-key` returns its trimmed `api-key`; the same call with the Secret absent, the key absent, or an empty value returns an error naming `mem0/mem0-admin-key` and not containing the value | automated: `go test ./internal/cli/servercmd -run Memory` | |
| 2 | A memory misconfiguration fails at load and says what is wrong | `memory.enabled` without `url`, with a non-http URL, without `apiKeySecret.namespace`, or under executor docker or process, each fails `config.Parse` with an error naming the key; an unset section loads with memory off | automated: `go test ./internal/config -run Memory` | |
| 3 | Tapas can add, search, list, get and delete one person's memories in the real mem0 pack | Against the mem0 pack server image (allowlist on) with pgvector and the fake model in Docker: an add for alice, then search and list for alice find it, get by id returns it with alice's id, delete removes it, and a get afterwards is `ErrNotFound` | automated: `bash scripts/test/mem0-integration.sh` (not in CI; Docker and a mem0 pack checkout at a pinned commit); narrated: its transcript | |
| 4 | One person can never read or delete another person's memory through the service | With memories for alice and bob seeded in `memorytest.Fake`: `Get(alice, bobsID)` and `Delete(alice, bobsID)` return `ErrNotFound` and the fake records no delete call; `Search`/`List` for alice never return bob's memory even when the client returns it; an empty owner is refused before any client call | automated: `go test ./internal/memory -run Service` | |
| 5 | mem0's key never appears in logs or errors | Client tests drive every error path (401, 404, 500, oversized body, transport failure) and assert no error string contains the key; key-read tests assert the same; the probe's log line carries no key | automated: `go test ./internal/memory ./internal/cli/servercmd` | |
| 6 | The gateway says at start whether mem0 answers with the key | The probe against a fake mem0 that accepts the key logs `memory.ready`; one that answers 401 logs `memory.unreachable` with the status; the gateway keeps running in both cases | automated: `go test ./internal/memory -run Probe` | |
| 7 | Turning memory off changes nothing | With no `memory` section the gateway reads no Secret (the fake client records no Get) and `wiring.Memory` returns nil; existing tests pass unchanged | automated: `go test ./internal/cli/... ./internal/config` and the full `make test` | |
| 8 | An admin can set it up from the docs | `site/src/content/docs/admin/memory.md` covers installing the mem0 pack with Tapas as caller (ServiceAccount by engine, pod labels, namespace), the config section, the start log lines and the limitations; the site builds | narrated: page headings and the site build output | |
| 9 | The PR proves itself in CI | Go build, vet, local build, tests (SQLite and Postgres shards) and govulncheck pass on the PR | narrated: `gh pr checks` transcript | |

## Open questions (for later work, not blocking)

1. **Global versus project memory.** The parent design stores global memory with `user_id` only and project memory with `user_id` and `agent_id`, and its journey 15 says one agent's project memory stays out of the person's other agents. mem0 v2.2.1 searched with `user_id` alone returns every memory with that user, project ones included, and the mem0 pack's allowlist refuses the operator filters that could exclude them. Recall needs a convention: a reserved `agent_id` for global memory, or filtering results by `agent_id` in Tapas. Recall's design decides it.
2. **Which memories are global.** Learning sends an exchange for an owner and an agent; nothing yet says when a learned fact is global rather than project. Also recall's design.
