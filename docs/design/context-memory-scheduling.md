# Context Memory Scheduling Requirements & Roadmap

## 1. Purpose

Why this document exists:

1. A production Discuss session drove the Server past its 8 GiB memory limit and was
   OOM-killed. Before landing fixes, we need agreement on what a *correct* memory
   discipline for turn-context assembly looks like — otherwise each fix only moves the
   cliff.
2. It is the acceptance baseline for a series of implementation PRs (see §7). Like
   `session-runtime-requirements.md`, requirements here are written against externally
   observable behavior so tests do not end up shaped by the current implementation.
3. It records the verified current state (§3) so future readers can tell which copies,
   caches, and unbounded loads were deliberate and which were accidents.

In this document, **MUST** marks a merge requirement. **MAY** marks an implementation
choice; whichever behavior is chosen must be observable and testable.

Throughout this document, *memory* means Server **process memory (RSS)** — not the agent
long-term memory subsystem in `internal/memory/`.

Code anchors below are verified against `main` @ `8382d42eb` (2026-08-25). Function
names are authoritative; line numbers will drift.

## 2. Incident and problem boundary

A long-lived Discuss thread (~8,000 history messages, ~5,200 timeline events, zero
compaction artifacts) produced a composed context estimated at ~1.74 M tokens. Every
byte of it was materialized, copied across multiple representations, serialized
repeatedly, and shipped toward the provider; the process crossed 8 GiB RSS and was
OOM-killed. Because the Server container runs with `pid: host` and `privileged: true`,
an OOM kill has host-level blast radius.

The OOM is not a single bug. Three orthogonal defect classes stack:

1. **Every guard runs after unbounded materialization.** Admission, budget trimming,
   and compaction all execute — when they execute at all — on data that is already
   fully resident.
2. **One context, many copies.** The same logical context is materialized in several
   representations per turn, with multiple full JSON serialization round-trips of the
   raw message payloads.
3. **No process-level memory scheduling.** Unbounded DB loads, process-lifetime caches
   with no eviction, O(N²) replay, per-token-delta snapshot cloning, and no
   `GOMEMLIMIT` or container memory limit anywhere.

Fixing any single class only delays the crash. This document requires all three to be
closed.

## 3. Verified current state

### 3.1 Guards run after materialization (timing inversion)

The Discuss native chain:

1. `internal/channel/discuss/worker.go` `handleReplyWithTurn` loads **all** turn
   responses (`history.Load` → `ListActiveSinceBySession` since the Unix epoch — the
   Discuss history adapter is explicitly unbounded, unlike the chat pipeline's 24-hour
   window in `service_history_messages.go`).
2. `internal/channel/discuss/trigger.go` `Build` →
   `timeline.ComposeContextWithArtifacts` (`internal/chat/timeline/context.go:235`)
   fully materializes `[]ContextMessage` with **no admission check of any kind**. The
   `EstimatedTokens` it computes is consumed by exactly one call site repo-wide: a
   `slog.Int` in `worker.go`. It is never compared against anything.
3. `internal/agent/application/turn_discuss.go` converts to SDK messages and collects
   source fragments; only then does `internal/contextview/provider_run_config.go`
   apply the `ContextBudgetPlan` (from PR #1039) at the provider stage — after the
   entire context has been materialized and copied several times.
4. Compaction runs **after** the turn: `maybeCompactDiscuss`
   (`turn_discuss.go`) is dispatched with `go` on `context.WithoutCancel` once the
   stream has drained.

Three aggravating facts:

- **No synchronous pre-turn compaction exists on the Discuss or pipeline-chat paths.**
  `runCompactionSync` (`internal/agent/application/service_compaction.go:167`, hard
  backstop at 75% of the context budget) has exactly one non-test caller
  (`service.go`, inside the legacy non-pipeline `prepareHistoryContext` branch). The
  `usePipeline` branch and all Discuss paths reach the model with zero synchronous
  backstop. The pipeline chat path at least has `trimPipelineMessagesByTokens`
  (post-materialization trim); the Discuss trigger path has no analogue at all.
- **A missing model context window disables all size control.**
  `providerContextBudgetPlan` (`internal/contextview/provider_run_config.go:238`)
  returns a nil plan when `ContextBudgetMaxTokens == 0`; the fragment selector then
  performs no budget drops and the full context goes to the provider (warn + audit
  ledger entry only). The same zero also disables compaction entirely:
  `autoCompactionThreshold` / `hardCompactionThreshold` return 0 and `maybeCompact`
  skips (`service_compaction.go`).
- **Artifact-load failure silently degrades to uncompacted composition.**
  `loadArtifacts` (`internal/channel/discuss/worker.go`) logs a warning and returns
  nil on error; `ComposeContextWithArtifacts` then re-materializes the entire raw
  history that compaction had already covered — the most dangerous possible
  degradation direction. The chat pipeline's `loadTimelineArtifacts`
  (`service_history_messages.go`) has the same shape.
- **The ACP Discuss path is unbounded by construction.**
  `discussACPFullContextPrompt` (`turn_discuss.go`) concatenates every message into a
  single prompt string with no cap; it is passed as `Query`, which no existing budget
  or backstop measures.

### 3.2 One context, many representations

Representations materialized per Discuss turn, in order:

| # | Representation | Location | Copy cost |
|---|---|---|---|
| 1 | `[]ContextMessage` | `timeline/context.go` `materializeMergeEntries` | RC text pieces coalesced via `strings.Builder` into new full-size strings; `RawContent` passed by slice header (no copy) |
| 2 | `[]turn.DiscussMessage` | `channel/discuss/trigger.go` | Header copy (cheap) |
| 3 | `[]sdk.Message` | `turn_discuss.go` `discussMessagesToSDK` | **Full `json.Marshal` + `json.Unmarshal` round-trip per RawContent message** |
| 4 | `[]contextfrag.ContextFrag` (source frags) | `internal/contextview/collector_discuss.go` `discussContextMessageToSDK` | **A second identical per-message marshal/unmarshal round-trip**; tail messages pay it twice via `latestComposedUserMessageIndex` |
| 5 | Compiled frags + rendered assembly | `internal/agent/context/fragment/compile.go` `CompileFrags` + `Render` | Two `cloneMessage` passes — O(parts) header copies, payloads shared |
| 6 | Provider view + step reselection | `internal/contextview/provider_run_config.go`, `provider_step_reselection.go` | Prefix `cloneSDKMessages` per changed attempt; **up to `len(frags)+2` full `ProviderPayloadHashAndBytes` serializations of the whole payload per step**, plus one per-fragment serialization per step in `applyProviderStepSerializedCosts`. *In-flight fix: #1072 makes every envelope decision price through the estimator and `ProviderPayloadHash` hash-only.* |
| 7 | Terminal persistence | `turn_discuss.go` → `internal/messageconv` → `service_store.go` | **Four serializations**: `event.Messages` + a retained `json.Marshal(event)` terminal payload, `json.Unmarshal` back to SDK messages, then a per-message marshal/unmarshal in `messageconv` before the DB write |

The genuinely payload-doubling steps are the three JSON round-trips (#3, #4, #7); the
frag clones (#5, #6) are allocation churn but share payload bytes.

### 3.3 No process-level memory scheduling

- **Process-lifetime timeline cache with no eviction.** `Pipeline.sessions` and
  `Pipeline.rendered` (`internal/chat/timeline/pipeline.go`) only ever insert or
  overwrite. `DropSession` exists with **zero callers repo-wide**. Every warmed
  session's full intermediate context (all nodes) and rendered context stay resident
  for the process lifetime, growing without bound in session count. This is the
  largest standing leak.
- **O(N²) replay.** `ReplaySession` folds `Reduce` over all events; `Reduce` starts
  with `cloneIC`, which reallocates the node slice per event (sizes 1..N → O(N²)
  allocated bytes). On the live path, `PushEvent` additionally re-runs `Render` over
  every node on every event — O(N²) over a session's life.
- **Unbounded DB loads.** `ListSessionEventsBySession`
  (`db/postgres/queries/session_events.sql`) has no LIMIT; `LoadEvents`
  (`timeline/persistence.go`) materializes every row, and `replayPipelineSession`
  feeds them all into the O(N²) replay. `ListUncompactedMessagesBySession`
  (`messages.sql`) has no LIMIT — compaction's token cap is applied in Go *after* the
  full load. `ListHistoryTurnsByBot` aggregates a bot's entire history with no LIMIT.
  (A bounding precedent already exists: `ListMessagesBySession` ends with
  `LIMIT 10000`.)
- **Per-token-delta snapshot cloning.** The session runtime memory backend clones the
  full message set **twice per accepted stream event** — including every non-empty
  text/reasoning delta — via per-message JSON round-trips
  (`internal/agent/runtime/session/memory.go` `cloneSnapshot` → `cloneUIMessages`).
  O(total payload bytes) marshalled per token delta ⇒ O(messages²) transient
  allocation over a run. The Redis backend has the same per-delta churn plus network.
  The memory backend's TTL purge explicitly refuses to evict active local runs.
- **No memory ceiling anywhere.** `GOMEMLIMIT`, `GOGC`, `debug.SetMemoryLimit`:
  zero hits repo-wide (code, Dockerfiles, compose, mise, entrypoints). No compose
  file sets `mem_limit` or `deploy.resources` for the `server` service. The Go GC's
  only wall is the 8 GiB cgroup kill.

### 3.4 Split estimation authority

Two token estimators disagree by ~2× on the same content:

- `internal/chat/timeline/context.go`: `charsPerToken = 2` over `len(RawContent)`
  (raw JSON bytes) — used for the (unused) composition estimate.
- `turn_discuss.go` `discussCompactableTokens` and the application/history paths:
  `len/4` — used to *trigger* compaction.

The smaller (÷4) figure drives compaction while budgets elsewhere assume the larger,
so compaction persistently triggers later than budget pressure implies.

### 3.5 Existing configuration surface (for reference)

- Compaction is **per-bot, in Postgres**, not TOML: `compaction_enabled`,
  `compaction_threshold` (DB default 0 since migration `0123`),
  `compaction_target_percent`, `compaction_model_id`
  (`internal/settings/types.go`). Derived thresholds are hardcoded constants
  (soft 50% / hard 75% / target 40%, `service_compaction.go`).
- `[agent]` TOML: `tool_output_max_bytes`, `tool_output_max_lines`,
  `system_files_max_bytes`, `context_loop_reselect` (`internal/config/config.go`).
  There is **no server-side context-size or admission knob today**.
- `[session_runtime]` TOML: `state_ttl` (default 24 h) is the only retention bound on
  session snapshots, and it never evicts active runs.
- `DefaultRecentProtectTokens = 20000`
  (`internal/contextview/provider_run_config.go`), per-run override only.

## 4. Correctness requirements

### CM-ADM-001: Admission precedes materialization

Before any full context materialization (string concatenation, JSON serialization, SDK
message construction), the system MUST make an admission decision based on metadata
only: message counts and pre-computed byte/token measures.

The admission budget MUST be
`min(model context window − output reserve, server-side absolute cap)`.

- The absolute cap MUST apply even when the model has no configured context window.
  `ContextBudgetMaxTokens == 0` MUST NOT disable size control (fixes the
  `providerContextBudgetPlan` nil-plan semantics).
- The check MUST run on **all** paths that reach a provider: Discuss native, Discuss
  ACP (including the concatenated prompt), pipeline chat, and legacy chat.

Admission measures MUST be obtainable without materializing message payloads. For
DB-backed history, counts and byte sums MUST come from metadata-only queries
(SQL-side aggregation, e.g. `COUNT(*)` + `SUM(octet_length(...))` over the
uncompacted range) or from already-persisted size measures — never from loading the
full payloads in order to measure them. Coarse SQL aggregates are an acceptable
admission source ahead of the per-fragment persisted costs required by CM-REP-002;
that coarse measurement infrastructure is part of PR 1, not PR 4 (see §7).

### CM-ADM-002: Deterministic over-budget trimming

When the raw context exceeds the admission budget, the system MUST deterministically
retain, in priority order: system fragments, the current triggering message(s), the
active compaction artifact summary, and the most recent history window that fits.
Older history MUST be dropped with recorded drop reasons (the existing
`SelectionDecision` / audit-ledger vocabulary).

If even the protected set does not fit, the turn MUST fail closed with a stable,
user-visible `context_too_large` error code (standard error-code conventions), not an
unbounded provider call and not a silent partial send.

### CM-ADM-003: Degradations must shrink, never grow

When the compaction-artifact frontier cannot be loaded, the system MUST NOT silently
recompose the full uncompacted history. It MUST either fail the turn with a stable
retryable error or degrade **downward** into CM-ADM-002 trimming. This applies to
`loadArtifacts` (Discuss) and `loadTimelineArtifacts` (pipeline chat) alike.

### CM-EST-001: Single estimation authority

One token-estimation function (one formula, one safety factor) MUST be used for
composition estimates, compaction triggers, and budget enforcement. Two subsystems
MUST NOT disagree about the size of the same content. Divergent callers
(`charsPerToken = 2` vs `len/4`) MUST converge on the shared estimator.

### CM-CMP-001: Synchronous pre-turn compaction backstop

On every provider-bound path, when compactable pressure meets the hard threshold, the
system MUST run compaction synchronously **before** the model call: compact → reload
artifacts → recompose → re-run CM-ADM-001/002 → then call the provider. The existing
single-flight / epoch / cooldown semantics of `runCompactionSync` apply.

Compaction success, failure, cooldown, and no-op MUST all still pass through
CM-ADM-001/002 — compaction is an optimization ahead of the hard boundary, never a
substitute for it.

### CM-CMP-002: Recovery entry point for oversized sessions

There MUST be a way to compact a specific session without triggering a new turn, so an
operator can recover a session that has already grown past safe size.

### CM-CMP-003: Backstop latency must be measured and gated

The synchronous backstop adds a full LLM summarization call to turn latency (seconds
to minutes), and Discuss group conversations are latency-sensitive. Therefore:

- The backstop MUST fire only at the hard threshold; the soft-threshold path stays
  asynchronous. In steady state (async compaction keeping up), the synchronous path
  is a rare last resort, not the common case.
- Backstop runs MUST be measurable in turn-latency terms (count, duration, p50/p95
  impact on affected turns) — see CM-OBS-001.
- Rollout MUST be gatable (per-bot setting or server config), with a shadow mode
  that logs would-have-fired decisions without blocking, so latency impact can be
  assessed before enforcement is enabled everywhere.

### CM-RPL-001: Bounded replay

Session replay MUST NOT require loading all events unbounded:

- Replay MUST apply the compaction frontier first and skip covered events'
  payloads.
- The uncompacted tail MUST be loaded via pagination/keyset, not a single unbounded
  query (`ListSessionEventsBySession`, `ListUncompactedMessagesBySession`,
  `ListHistoryTurnsByBot`).
- Replay MUST operate under an explicit resident-bytes bound.

### CM-RPL-002: Incremental projection

Applying one event to the intermediate context MUST be O(event), not O(session):
no full node-slice clone per event (`cloneIC`) and no full re-render per event
(`PushEvent` → `Render`). Full re-rendering MAY still occur on explicit invalidation
(e.g. render-parameter changes).

Incremental rendering MUST be correct under mutation, not only under append:

- Dirty tracking MUST be per-node. A message edit, delete, or reaction can invalidate
  previously rendered segments — including coalesced neighbors — so a global
  "tail is dirty" model is insufficient.
- The incremental result MUST be verified against full re-rendering as an oracle: for
  any event sequence (including edits and deletes), the incremental output must be
  byte-equal to `Render` over the fully reduced context. This equivalence runs as a
  package-level property/fuzz test and MAY additionally run as a sampled shadow
  check behind a debug flag.

### CM-CCH-001: Bounded pipeline cache

`Pipeline.sessions` / `Pipeline.rendered` MUST be bounded by an eviction policy (LRU
and/or TTL and/or resident-bytes cap). Eviction MUST actually be wired (`DropSession`
or equivalent must have callers). A cold session MUST be recoverable via (bounded)
replay, so eviction is always safe.

Sequencing is a hard constraint: eviction MUST NOT be enabled before bounded replay
(CM-RPL-001) has landed. Evicting a session whose cold-start recovery is still an
unbounded full replay converts a standing leak into repeated load/evict thrash —
memory spikes plus CPU churn. The two mechanisms ship together, or replay-first.

### CM-REP-001: Single materialized representation, zero-copy raw payloads

Between composition and provider render, the context MUST exist as **one** source of
truth (refs + per-item size measures + drop reasons + artifact frontier — a
`DiscussContextPlan` or equivalent), with rendering into provider messages happening
**once**, after selection. `RawContent` (`json.RawMessage`) MUST be passed by
reference between representations; the current marshal/unmarshal round-trips in
`discussMessagesToSDK`, `discussContextMessageToSDK`, and `messageconv` MUST be
eliminated or collapsed into the single final render.

Zero-copy sharing requires an explicit immutability contract. Every consumer of a
shared `json.RawMessage` / `[]byte` payload MUST treat it as read-only; aliased-write
bugs on shared byte slices are among the hardest to diagnose in Go. The contract
MUST be enforced, not merely documented: a stated read-only convention at the
type/field level, plus test protection (e.g. tests that checksum shared buffers
before and after a full pipeline pass, run under `-race`). Any step that genuinely
needs to mutate MUST copy first.

### CM-REP-002: Incremental budget accounting

Per-step provider budget checks MUST NOT serialize the full payload O(n) times per
step. Each fragment MUST carry its serialized-cost measure (computed once, ideally
persisted), and selection loops MUST account by addition/subtraction over those
measures. A single final serialization per provider call is acceptable.

### CM-REP-003: Persistence without redundant round-trips

Terminal persistence MUST NOT round-trip the final messages through redundant
serializations. The terminal event's raw message bytes MUST flow to the store as
`json.RawMessage` (or equivalent), with at most one decode where the store schema
requires structured columns.

### CM-REP-004: Copy-on-write streaming snapshots

Session-runtime snapshot updates during streaming MUST NOT clone the full message set
per delta. Updates MUST copy only changed messages/fields (copy-on-write), bounding
per-delta work by the delta size, not the run size.

### CM-PRC-001: Process memory ceiling

The Server MUST run with `GOMEMLIMIT` set (recommended ≈ 85% of the container limit),
and the shipped compose/deploy configurations MUST set an explicit memory limit for
the `server` service. Reaching the soft limit must surface as GC pressure and
degraded admission (CM-ADM-002), not an OOM kill.

### CM-OBS-001: Observability

The following MUST be observable (metrics and/or structured logs with stable keys):

- oversized-admission events (count, estimated size, applied action);
- context size before/after materialization and the provider's final input tokens;
- synchronous-compaction backstop runs, outcomes (success/failure/cooldown), and
  per-run duration / turn-latency impact;
- replay event count and bytes; pipeline-cache evictions;
- `context_too_large` occurrences.

## 5. Target architecture (design layers)

| Layer | Mechanism | Requirements served |
|---|---|---|
| L1 Hard boundary | Metadata-based admission check ahead of `trigger.Build` / pipeline composition / ACP prompt build; deterministic trim; `context_too_large` fail-closed | CM-ADM-001/002/003, CM-EST-001 |
| L2 Pre-turn compaction | Synchronous hard-threshold backstop on Discuss native, Discuss ACP, and pipeline chat; then re-admission via L1; shadow-first gated rollout | CM-CMP-001/002/003 |
| L3 Bounded replay & caches | Frontier-first replay, paginated tail loads, incremental IC, render-on-demand, LRU/TTL/bytes-bounded pipeline cache | CM-RPL-001/002, CM-CCH-001 |
| L4 Representation convergence | `DiscussContextPlan` (refs + measures before selection; single render after), zero-copy `RawContent`, incremental budget accounting, raw-bytes persistence, copy-on-write snapshots | CM-REP-001/002/003/004 |
| L5 Process scheduling | `GOMEMLIMIT`, container memory limits, admission/compaction/eviction metrics | CM-PRC-001, CM-OBS-001 |

Design principles:

1. **Admission before materialization.** No large allocation may precede the budget
   decision that could reject it.
2. **Budget = `min(window − reserve, absolute cap)`**, with the absolute cap always
   live. A misconfigured model must degrade to a smaller context, never to an
   unbounded one.
3. **One source of truth, lazy rendering.** Until selection completes, only metadata
   exists; the selected set renders exactly once.
4. **Bounded resident state.** Every cache has an eviction policy; every replay has a
   resident bound; every unbounded query gains a LIMIT or keyset pagination.

## 6. Invariant (definition of done)

After all layers land, the process obeys:

> **Peak Server memory ≈ (rendered size of one selected, in-budget context) × (bounded
> turn concurrency) + bounded caches — independent of total history size.**

Every current violation of this invariant (full materialization before guards, the
representation copies, the never-evicted pipeline cache, O(N²) replay, per-delta
snapshot clones, unbounded queries) maps to a requirement in §4 and a roadmap item in
§7.

## 7. Delivery roadmap

### PR 1 (P0) — Pre-materialization hard boundary + process backstop

- [x] Admission check ahead of `trigger.Build`: max bytes / estimated tokens /
      message count / `min(window − reserve, absolute cap)`; absolute cap effective
      when the model window is unset (fixes `providerContextBudgetPlan` disable
      semantics at its application-level source) — CM-ADM-001.
      Landed as `timeline.ComposeContextWithArtifactsBudgeted` (entry-metadata
      selection before materialization), `[agent] context_absolute_max_tokens`
      (default 200k, never disabled), and `Service.effectiveContextTokenBudget`
      clamping every provider-bound budget.
- [x] Metadata-only admission measures and byte-budgeted history loads —
      CM-ADM-001, CM-REP-002. Landed as `MeasureActiveMessagesBySession`
      (SQL-side `COUNT(*)` + `SUM(octet_length(content::text))`) plus the
      `…WithinBytes` query variants that admit rows newest-first on the
      database side until the budget's byte equivalent is spent. Every
      DB-backed history load that feeds composition is bounded by the budget:
      the discuss TR load (previously unbounded since the Unix epoch), the
      pipeline-chat TR load, and the legacy `loadHistoryRecords` (both
      session- and bot-scoped). Total history size never bounds process
      memory; the budget does. Remaining resident-state bounds — the RC
      pipeline cache and event replay — are CM-CCH-001/CM-RPL-001 and land
      in PR 3; PR 4's persisted per-fragment costs refine the measures.
- [x] Deterministic over-budget trim (artifact summaries + newest message + recent
      contiguous window, no orphaned tool responses); `context.protected_overflow`
      stable error when even the protected set does not fit — CM-ADM-002. Both
      admission sites (channel-side compose, agent-side re-admission) delegate to
      the single `turn.AdmitContextEntries` core, which also fails closed when the
      newest entry is itself an orphaned tool response instead of silently
      admitting an empty or summaries-only window.
- [x] `loadArtifacts` / `loadTimelineArtifacts` failure: degrade into the bounded
      admission window with a stable `context_admission_degraded` log; never silent
      unbounded recomposition — CM-ADM-003
- [x] Unify token estimation — CM-EST-001. The authority is `contextfrag`'s
      `EstimateBytesPerToken` from the unified context-budget work (#1012).
      For the packages the architecture guards keep out of `agent/context`
      (chat/timeline, channel), the turn port re-exports the vocabulary
      (`turn.ContextBytesPerToken`, `turn.EstimateTokensFromBytes`,
      `turn.DefaultContextCapTokens`) as thin aliases of the `contextfrag`
      definitions, so the chars/2 vs len/4 split is gone and a real tokenizer
      swaps every consumer together.
- [x] `GOMEMLIMIT` + compose memory limits for `server` (production and devenv) —
      CM-PRC-001
- [x] Observability: `context_admission` / `context_admission_rejected` /
      `context_admission_degraded` structured logs with stable keys (estimated,
      selected, budget, dropped) on discuss compose, discuss turn, ACP prompt, and
      pipeline paths — CM-OBS-001

### PR 2 (P0) — Synchronous pre-turn compaction

- [ ] `pumpDiscussNative` / `pumpDiscussACP`: compute raw compactable pressure before
      the model call; ≥ hard threshold → synchronous compaction (reuse
      `runCompactionSync` single-flight/epoch/cooldown) → reload artifacts →
      recompose → re-admission — CM-CMP-001
- [ ] Compaction failure / cooldown / no compaction model → fall into L1 trim —
      CM-CMP-001
- [ ] Same synchronous backstop for the pipeline chat path
      (`buildMessagesFromPipeline`) — CM-CMP-001
- [ ] Budget the ACP prompt before concatenation (`discussACPFullContextPrompt`) —
      CM-ADM-001
- [ ] Session-targeted compaction entry point that does not trigger a turn —
      CM-CMP-002
- [ ] Latency impact assessment + gated rollout: shadow mode first (log
      would-have-fired without blocking), per-bot/config gate, p50/p95 turn-latency
      metrics for backstop runs — CM-CMP-003
- [ ] Adapt the compaction input to the Discuss corpus explicitly (turn responses +
      timeline vocabulary, unified estimator). Do not assume `runCompactionSync` is
      a drop-in: its input is plain history messages; only its
      single-flight/epoch/cooldown semantics carry over unchanged — CM-CMP-001,
      CM-EST-001

### PR 3 (P1) — Bounded replay + pipeline cache eviction

Landing order inside this PR is a hard constraint: paginated, frontier-first replay
ships before — or atomically with — cache eviction, never eviction first
(CM-CCH-001).

- [ ] `Pipeline.sessions`/`rendered`: LRU + TTL + resident-bytes bound; wire
      `DropSession` — CM-CCH-001
- [ ] Remove per-event `cloneIC` full copy (incremental IC or append-only nodes);
      remove per-event full `Render` with per-node dirty marking (edits/deletes
      invalidate affected nodes and coalesced neighbors, not a global tail) —
      CM-RPL-002
- [ ] Incremental-render equivalence oracle: property/fuzz test asserting
      byte-equality with full re-render over event sequences including edits and
      deletes — CM-RPL-002
- [ ] `ListSessionEventsBySession` pagination/keyset; replay applies the compaction
      frontier before loading covered payloads — CM-RPL-001
- [ ] `ListUncompactedMessagesBySession` / `ListHistoryTurnsByBot`: LIMIT or
      token-budgeted incremental loading — CM-RPL-001
- [ ] Metrics: replay event count/bytes, cache eviction counters — CM-OBS-001

### PR 4 (P1) — Representation convergence & zero-copy

- [ ] Introduce `DiscussContextPlan`: refs + measures before selection; single render
      of the selected set — CM-REP-001
- [ ] Eliminate `RawContent` marshal/unmarshal round-trips (`discussMessagesToSDK`,
      `discussContextMessageToSDK`) — frags reference `json.RawMessage` bytes
      directly, under the read-only contract with checksum test protection —
      CM-REP-001
- [ ] Step reselection: stop serializing the full payload per attempt —
      CM-REP-002. **Defer to #1072** (`ProviderEnvelopeTokens`: every envelope
      decision prices through the estimator, `ProviderPayloadHash` becomes
      hash-only), which is in flight in the context-budget landing stack;
      this roadmap only adds persisted per-fragment costs on top if a gap
      remains after it lands, rather than re-implementing the accounting.
- [ ] Terminal persistence: pass `json.RawMessage` through to the store; drop the
      decode/re-encode chain (`turn_discuss.go` → `messageconv`) — CM-REP-003
- [ ] Snapshot backend: copy-on-write per-delta updates instead of full
      `cloneSnapshot`/`cloneUIMessages` (memory and Redis backends) — CM-REP-004

### PR 5 (P2) — Acceptance tests & benchmarks

- [ ] Compaction disabled + 5,000 events: no OOM; hard boundary engages
- [ ] Compaction enabled over threshold: synchronous compaction before recomposition;
      failure/cooldown/no-model → safe trim
- [ ] `context_window = 0` or grossly mismatched: absolute cap engages
- [ ] Post-artifact message edits preserved; concurrent new messages during
      compaction do not skew the frontier
- [ ] Production-scale regression (8,000 history messages / 5,000 events / zero
      artifacts): record the pre-fix baseline peak, then assert (a) **decoupling** —
      peak RSS at 2× history scale stays within a small constant factor (≤ ~15%) of
      peak at 1×, per the §6 invariant — and (b) a **relative reduction** vs the
      recorded baseline, with the percentage target fixed once the baseline run is
      recorded. Absolute byte figures are reported for visibility, not used as the
      merge gate.

### Downstream (non-OSS)

Cloud deploy tracks this work separately: submodule sync, Helm memory
limits/`GOMEMLIMIT` for the server, and alerting on oversized-admission and
compaction-failure metrics.

## 8. Acceptance levels

### 8.1 Black-box acceptance

Black-box scenarios MUST use the real `cmd/agent` Server, real PostgreSQL, the public
HTTP/WebSocket surface, and a controllable fake model service that records request
sizes. Peak-memory assertions run the Server as a separate process and read its RSS /
`runtime` metrics; they MUST NOT be derived from unit-level allocation counters alone.

Acceptance tests MUST NOT construct composition internals (`Pipeline`, selectors,
stores) directly — that would prove internal collaboration, not the invariant.

### 8.2 Package-level tests

Package tests cover: admission arithmetic and trim determinism, estimator
convergence, frontier-first replay, cache eviction, incremental accounting equality
with full serialization (shadow comparison), and copy-on-write snapshot semantics.
They complement but never replace black-box acceptance.

## 9. Acceptance scenarios

| Scenario | Setup | Primary assertion | Requirements |
|---|---|---|---|
| oversized cold session | 8k messages, 5k events, no artifacts, compaction off | Turn completes with trimmed context or `context_too_large`; provider request ≤ budget; peak RSS bounded | CM-ADM-001/002, CM-PRC-001 |
| missing context window | model with `context_window = 0` | Absolute cap enforced; no unbounded provider call | CM-ADM-001 |
| artifact store outage | frontier load fails on long thread | No silent full recomposition; controlled degrade or retryable error | CM-ADM-003 |
| pre-turn backstop | compaction on, pressure ≥ hard threshold | Compaction runs before the model call; recomposed context in budget | CM-CMP-001 |
| backstop latency & rollout | backstop behind gate, shadow vs enforced | Shadow logs decisions without blocking; enforced mode's p50/p95 turn-latency impact measured | CM-CMP-003 |
| backstop unavailable | compaction fails / cooldown / no model | L1 trim engages; turn still bounded | CM-CMP-001, CM-ADM-002 |
| ACP long thread | Discuss ACP, oversized history | Prompt built within budget; no unbounded `Query` | CM-ADM-001 |
| replay at scale | cold start with 5k events | Paginated loads; frontier skips covered payloads; bounded replay memory | CM-RPL-001/002 |
| cache pressure | many concurrent sessions | Evictions occur; evicted sessions recover via replay; resident bytes bounded | CM-CCH-001 |
| streaming long run | long tool-loop run with streaming | Per-delta work bounded by delta size; no O(messages²) churn | CM-REP-004 |
| memory scaling | same scenario at 1× and 2× history scale | Peak RSS at 2× within a small constant factor of 1× (§6 invariant) | §6 |
| incremental render oracle | event sequences incl. edits/deletes | Incremental render byte-equal to full re-render | CM-RPL-002 |
| equivalence guard | same inputs, old vs new path | Selected/rendered provider payload byte-identical (or semantically equal under a documented normalization); shared buffers unmodified (checksum) | CM-REP-001/002/003 |

## 10. Pass criteria

The context memory scheduling work is complete when, simultaneously:

1. Every **MUST** in §4 has a corresponding implementation or an explicitly documented
   deployment boundary.
2. The §6 invariant holds empirically in the production-scale regression: peak RSS
   at 2× history scale stays within a small constant factor of peak at 1×, and the
   relative reduction vs the recorded pre-fix baseline meets the target fixed at
   baseline time. Absolute byte figures are reported, not gated on.
3. Black-box acceptance passes for every scenario in §9.
4. `GOMEMLIMIT` and container memory limits ship in the default compose deployment.
5. No provider-bound path exists that can materialize an unbounded context — verified
   by an architecture guard test (in the spirit of `internal/arch/`) or an equivalent
   mechanical check.
