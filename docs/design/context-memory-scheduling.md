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

### 3.3 Process-level memory scheduling baseline (resolved by PR 3)

- **Timeline cache is bounded.** `Pipeline` now applies LRU, TTL, session-count,
  and approximate resident-byte limits. Session delete/history reset explicitly
  invalidate entries; bot-wide reset conservatively clears the cache.
- **Projection and replay are incremental.** Live events mutate the owned IC and
  re-render only dirty/new nodes, while cold replay reduces in place and renders
  once. The public pure `Reduce` + full `Render` path remains the equivalence
  oracle.
- **Runtime DB loads are bounded.** Timeline replay uses a newest-first keyset and
  byte budget, excluding in SQL what the session's history checkpoint stands for
  before those payloads cross the process boundary. Compaction reads its
  candidates oldest first within a byte bound; the legacy bot turn aggregate has a
  defensive LIMIT.
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

- Compaction has no settings. A run compacts only when its request does not
  fit, with the model the run resolved. The former per-bot
  `compaction_enabled` / `compaction_threshold` / `compaction_target_percent` /
  `compaction_model_id` columns, the derived soft/hard/target thresholds, and
  `[agent] sync_compaction` were removed (CM-CMP-001/003).
- `[agent]` TOML: `tool_output_max_bytes`, `tool_output_max_lines`,
  `system_files_max_bytes` (`internal/config/config.go`).
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

### CM-ADM-002: Over-budget context is compacted, never trimmed

When the context a request needs exceeds the admission budget, the system MUST NOT
drop history, current input, or summaries to make it fit. It compacts once
(CM-CMP-001) and rechecks:

- If even the protected set — system fragments and the current triggering input —
  does not fit, the run MUST fail with the stable `context.protected_overflow` code.
- If the compaction fails, does not reduce the context, or leaves it still too
  large, the run MUST fail with the stable `context.budget_unsatisfied` code.

Both failures are bounded: no provider call, no partial send, and the triggering
input stays unconsumed. Inside a run the same holds for every step request: a tool
result is bounded once, when the tool returns, and no later request prunes, stubs,
or drops it. Input that composition cannot carry (Channel admission
records it as omitted) MUST be compacted by that request, or the run fails; it is
never silently skipped. Bounded reads (CM-ADM-001) still limit what a request loads,
but a compaction reads every uncompacted row of the thread, loaded or not: rows that
slid out of a byte-bounded view are folded, never skipped. History does not age out
by time. What lies before the session's history checkpoint reaches a request as the
rolling summary or as a raw row kept beside it (a run's inputs and steers); what lies
after it stays raw, and each compaction commits a new checkpoint by folding the
summary with the oldest foldable units after it. An edit or retry compacts and renders only the
history before its cutoff.

### CM-ADM-003: A missing source fails the request

When a source the request needs cannot be loaded — the session's history
checkpoint, a row it keeps by reference, or the persisted turn history — the system
MUST fail the request with a stable retryable error and leave its input unconsumed.
It MUST NOT silently recompose the full uncompacted history, and it MUST NOT
continue with a smaller context that leaves the missing content out. This applies
to Discuss (`timeline.CheckpointSource.Load`, the history reader), pipeline chat
(`timeline.CheckpointSource.Load`, `loadTurnResponses`) and record chat
(`loadHistoryRecords`) alike. A load the byte budget truncated is not a smaller
context to continue with: it is compacted before the request is built
(CM-CMP-001).

### CM-EST-001: Single estimation authority

One token-estimation function (one formula, one safety factor) MUST be used for
composition estimates, compaction triggers, and budget enforcement. Two subsystems
MUST NOT disagree about the size of the same content. Divergent callers
(`charsPerToken = 2` vs `len/4`) MUST converge on the shared estimator.

### CM-CMP-001: Synchronous compaction when a request does not fit

On every provider-bound path, when the request does not fit its budget, the run MUST
compact synchronously before the model call: compact → reload the history checkpoint
→ rebuild → recheck (CM-ADM-001/002) → call the provider. A history load the byte
budget truncated compacts the rows it left out the same way before the request is
built, and loads again; a load still truncated fails the request. Durable native runs also reach this
point after a committed tool batch and before the next provider call, where the event
splices the run's own request instead of rebuilding it. This is a trigger point, not a
run-scoped candidate boundary: the event can fold the rendered active summary,
pre-run history, and earlier committed steps of the current run. Compaction is one
event per pending request:

- The event folds everything the request does not keep raw into one rolling summary.
  It does not stop once the request would fit, and it does not aim at a fixed target,
  ratio, or watermark.
- The summarizer is the model the run resolved. A fork of the run's request keeps the
  run's output settings, so it reads only what the window holds beside the run's
  output allowance and a provider setting sized for that output (a thinking budget)
  stays valid; a request built from rows asks the compaction model for what the
  window leaves, up to its capability. The stated bound prices the summary wrapper
  and provider envelope. The prompt states the capacity facts — the
  content being replaced, what stays verbatim, the room available, the summary's
  upper bound, and the current goal — and the model decides what to keep and how long
  the summary is. Below the minimum useful summary room, the event fails before a
  model call.
- History is never dropped to make the summarizer call fit. When the history to
  replace does not fit one summarizer call, the event folds the oldest prefix that
  fits, in as many passes as reading it takes; each pass absorbs the summary before
  it and commits a new history checkpoint. A call group, which a pass reads whole
  or not at all, larger than one call can read reaches the summarizer by the head
  and tail of its larger parts, which share the call's room; a group no cut brings
  within the call fails the event.
- Afterwards the request is only rechecked, never compacted again to reach a lower
  mark. A Discuss batch gets one recovery; the recomposed attempt only rechecks.
- What stays verbatim is decided once per event from one budget, a quarter of
  what the allowance leaves beside the system prompt, tools and runtime context, at
  most 32000 tokens: the input or steers the next call answers stay
  whatever they take, then older anchors newest first, then the newest complete call
  groups. The latest step and a decision continuation's deferred call and answer are
  call groups like any other: within the budget they stay raw, beyond it they fold
  with what is before them. Anchors do not create separate summary segments.
- In-run candidates are the request's messages, named by the history rows,
  inbound messages or summaries they render. A pass replaces whole messages; the
  messages it keeps remain byte-identical. One placement rule (`turn.PlaceSummary`)
  orders the request a pass leaves: the messages it keeps inside the folded span
  come before the summary, in the order its checkpoint keeps them. The run's
  splice and a request built from rows apply that order, and every later load
  renders the checkpoint's items the same way.
- In-run recovery splices the request in memory. Each pass forks the request the
  run sends, without reloading raw history or ancestor summary payloads. A pass
  refuses a request composed on another checkpoint. The commit is fenced by run
  ownership and moves the session's checkpoint only from the one the request was
  composed on; cancellation prevents a successful publication.
- The run identity, step limit, loop guard and committed effects survive recovery.
  Idle detection pauses during compaction; cancellation and execution budgets still
  apply. Between events, the provider request only appends.
- On success the summary is committed and the run continues. Summarizer failure or
  exhausted retries, an empty, truncated, or invalid summary, a summary that does not
  reduce, a context that still does not fit, a stale snapshot, or a failed commit all
  end the run with `context.budget_unsatisfied` (CM-ADM-002), or
  `context.protected_overflow` when what stays raw alone does not fit, with a
  structured reason. A failed pass never replaces context or moves the checkpoint;
  passes before it stay committed. Armed runs never fall back to step dropping or
  512-byte result placeholders.
- Web, IM, trigger, follow-up, decision continuations, retry, edit and native
  Discuss are armed through shared native binding when they have durable
  ownership. Subagents retain their separate capability boundary: their step
  requests only grow, and a request that does not fit ends with
  `context.budget_unsatisfied`. External runtimes own their context.

### CM-CMP-002: Oversized sessions recover on their next request

A session that has grown past its budget MUST recover on its next request through
CM-CMP-001. No separate operator entry point is kept: the `/compact` command and the
HTTP compaction endpoint were removed with the compaction policy revision.

### CM-CMP-003: Compaction must be measured

A synchronous compaction adds summarizer calls to the latency of the run that needs
it. It fires only when the request does not fit: there are no soft/hard thresholds,
background compaction, cooldown, mode switches, or shadow mode. Each compaction MUST
record its outcome, pass count, and duration under stable log keys
(`compaction finished` / `compaction failed`; `context compacted` for an in-run
event, which also records the request tokens before and after and the allowance).
A failed in-run event returns its cause to the run's result-record boundary, and
`MutationContextSwitch` keeps the trigger, pass count and failure reason in the
run's context lifecycle — see CM-OBS-001.

### CM-RPL-001: Bounded replay

Session replay MUST NOT require loading all events unbounded:

- Replay MUST start from the session's history checkpoint and skip the payloads of
  the events it stands for.
- The history after the checkpoint MUST be loaded via pagination/keyset, not a
  single unbounded query (`ListSessionEventsBySession`,
  `ListHistoryMessagesAfterWithinBytes`, `ListHistoryTurnsByBot`).
- Replay MUST operate under an explicit resident-bytes bound.
- The database work of a page MUST scale with the events it reads, not with the
  session's history: nothing a page computes may span the whole session.

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
truth (refs + per-item size measures + drop reasons + history checkpoint — a
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
bounded admission (CM-ADM-001/002), not an OOM kill.

### CM-OBS-001: Observability

The following MUST be observable (metrics and/or structured logs with stable keys):

- oversized-admission events (count, estimated size, applied action);
- context size before/after materialization and the provider's final input tokens;
- synchronous compactions: outcome, pass count, and duration;
- replay event count and bytes; pipeline-cache evictions;
- `context.protected_overflow` / `context.budget_unsatisfied` occurrences.

## 5. Target architecture (design layers)

| Layer | Mechanism | Requirements served |
|---|---|---|
| L1 Hard boundary | Metadata-based admission check ahead of `trigger.Build` / pipeline composition / ACP prompt build; fail closed with `context.protected_overflow` / `context.budget_unsatisfied`, never trim | CM-ADM-001/002/003, CM-EST-001 |
| L2 Compaction | One synchronous compaction when a request does not fit, on Discuss native, Discuss ACP, and chat; then recheck via L1 | CM-CMP-001/002/003 |
| L3 Bounded replay & caches | Checkpoint-first replay, paginated tail loads, incremental IC, render-on-demand, LRU/TTL/bytes-bounded pipeline cache | CM-RPL-001/002, CM-CCH-001 |
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
      Landed as `timeline.ComposeContextWithCheckpointBudgeted` (entry-metadata
      selection before materialization), `[agent] context_absolute_max_tokens`
      (default 200k, never disabled), and `Service.effectiveContextTokenBudget`
      clamping every provider-bound budget.
- [x] Metadata-only admission measures and byte-budgeted history loads —
      CM-ADM-001, CM-REP-002. Landed as `MeasureHistoryMessagesAfter`
      (SQL-side `COUNT(*)` + `SUM(octet_length(content::text))`) plus the
      `…WithinBytes` query variants that admit rows newest-first on the
      database side until the budget's byte equivalent is spent. Every
      DB-backed history load that feeds composition is bounded by the budget:
      the discuss TR load (previously unbounded since the Unix epoch), the
      pipeline-chat TR load, and the legacy `loadHistoryRecords` (both
      session- and bot-scoped). Total history size never bounds process
      memory; the budget does. The budget covers content only, so metadata
      is handled separately: both TR loads project content and the
      interrupted flag (`ListTurnResponseSourcesAfterWithinBytes`),
      and `loadHistoryRecords` receives metadata undecoded and decodes one
      row at a time. Remaining resident-state
      bounds — the RC pipeline cache and event replay — are
      CM-CCH-001/CM-RPL-001 and land in PR 3; PR 4's persisted per-fragment
      costs refine the measures.
- [x] *(Superseded: over-budget context is now compacted or fails, see the
      compaction policy revision.)* Deterministic over-budget trim (artifact summaries + newest message + recent
      contiguous window, no orphaned tool responses); `context.protected_overflow`
      stable error when even the protected set does not fit — CM-ADM-002. Both
      admission sites (channel-side compose, agent-side re-admission) delegate to
      the single `turn.AdmitContextEntries` core, which also fails closed when the
      newest entry is itself an orphaned tool response instead of silently
      admitting an empty or summaries-only window.
- [x] *(Superseded: a load failure now fails the request.)*
      `loadArtifacts` / `loadTimelineArtifacts` failure: degrade into the bounded
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

> Superseded in part by the compaction policy revision below: the thresholds, the
> shadow gate, the trim fallback, and the manual entry point are gone.

- [x] Discuss backstop before any model call (native and ACP): raw compactable
      pressure ≥ hard threshold → synchronous compaction → the run ends with
      `DiscussEventRecompose` and the driver reloads the history checkpoint,
      rebuilds the plan, re-admits, and resubmits (bounded retry cycles, cursor
      never advances on recompose) — CM-CMP-001. Landed as
      `Service.maybeSyncCompactDiscuss` + the worker recompose loop; the
      recompose signal travels the existing opaque event vocabulary, so no
      transport change. **Split-mode upgrade ordering:** a pre-recompose
      Channel binary decodes the nil-payload recompose event as a failed
      stream event; without the failed-native-turn cursor guard it advances
      the cursor and silently consumes the trigger message, and with that
      guard it merely fails closed and retries after the summary lands.
      Upgrade Channel before (or together with) Server; embedded all-in-one
      deployments upgrade atomically and are unaffected.
- [x] Compaction failure / cooldown / disabled settings / missing summarizer →
      the turn proceeds with the L1 admission-trimmed context — CM-CMP-001
- [x] Same synchronous backstop for the pipeline chat path (pressure measured in
      `buildMessagesFromPipeline`, recompose is a same-process rebuild) —
      CM-CMP-001
- [x] Budget the ACP prompt before concatenation — CM-ADM-001 *(landed in PR 1:
      `admitDiscussMessages` runs before `discussACPFullContextPrompt`)*
- [x] Session-targeted compaction entry point that does not trigger a turn —
      CM-CMP-002 *(already satisfied by the `/compact run` command,
      `internal/command/compact.go`: manual `RunCompactionSync` with full ratio,
      no agent turn started)*
- [x] Gated rollout: `[agent] sync_compaction = off | shadow | active`, default
      shadow (logs would-have-fired without blocking); active-mode backstop runs
      log status + duration + pressure/threshold under the stable
      `sync_compaction_backstop` key for latency assessment — CM-CMP-003.
      *(p50/p95 aggregation happens in the log pipeline; per-run duration_ms is
      the exported measure.)*
- [x] Compaction input adapted explicitly: discuss pressure comes from
      `discussCompactableTokens` over the composed context (artifact summaries
      excluded) in the unified estimator; only `runCompactionSync`'s
      single-flight/epoch/cooldown semantics are reused — CM-CMP-001, CM-EST-001

### Compaction policy revision — supersedes the PR 1 trim and PR 2 backstop

- [x] Compaction runs only when a request does not fit, synchronously inside the
      run, once per pending request, then rechecks — CM-CMP-001
- [x] Summarizer is the run's model; output limit from model capability; the
      prompt states capacity facts and the model decides the length; no target,
      ratio, 4096 cap, window fractions, or input truncation — CM-CMP-001
- [x] Every failure ends the run with `context.budget_unsatisfied` and no trim
      fallback; Discuss admission no longer drops input — CM-ADM-002
- [x] A checkpoint or history load failure fails the request instead of degrading —
      CM-ADM-003
- [x] Removed background compaction, soft/hard thresholds, cooldown, shadow/off/
      active modes, the compaction barrier, `/compact`, the HTTP endpoint, and the
      four per-bot compaction settings — CM-CMP-002/003
- [x] Compaction logs outcome, passes, and duration — CM-CMP-003, CM-OBS-001
- [x] Rows of the thread stay candidates when a byte-bounded view slides past
      them — CM-ADM-002
- [x] In-run compaction for durable native tool loops under the same rule — CM-CMP-001
- [x] Remove the step selector's in-run pruning, dropping, and rescue, and the
      `context_loop_reselect` rollout switch: a step request only grows, and a
      tool result is bounded once when produced, at 32 KiB by default
      (MEMOH-132, MEMOH-133) — CM-ADM-002
- [x] In-run compaction for Discuss: a native Discuss run writes each step as it
      commits, folds against the version of each source Channel rendered, keeps
      the batch inputs in place, and only its answer, never a step before it,
      proves the batch consumed — CM-CMP-001
- [x] In-run compaction for retried and edited turns: the attempt folds the
      history before its cutoff and its own steps, a retry's input follows the
      copy its first step writes, and that step moves the session back to the
      newest history checkpoint that does not depend on the replaced turn —
      CM-CMP-001
- [ ] In-run compaction for subagents, which keep their own committer —
      CM-CMP-001
- [x] Thread history has no time window: compaction candidates are the rows the
      history checkpoint keeps and every row after its position, so history is
      summarized rather than dropped, and chat loads read the rows after the
      checkpoint newest first within their budget, so their cost follows what is
      unsummarized — CM-ADM-001/002
- [x] History checkpoints replace compaction artifacts: a session's context is
      its active checkpoint's ordered items, summaries and rows kept by
      reference, then its rows after the checkpoint's position. A fold commits a
      new checkpoint under run ownership, an edit or retry rewinds the pointer in
      the statement that replaces the turn, and a fork copies the checkpoint its
      turn keeps, so no coverage, epoch, claim, source hash or repair remains. A
      load the byte budget truncated folds what it left out before the request
      is built — CM-ADM-003, CM-CMP-001, CM-RPL-001

### PR 3 (P1) — Bounded replay + pipeline cache eviction

Landing order inside this PR is a hard constraint: paginated, checkpoint-first replay
ships before — or atomically with — cache eviction, never eviction first
(CM-CCH-001).

- [x] `Pipeline.sessions`/`rendered`: LRU + TTL + resident-bytes bound; wire
      `DropSession` — CM-CCH-001
- [x] Remove per-event `cloneIC` full copy (incremental IC or append-only nodes);
      remove per-event full `Render` with per-node dirty marking (edits/deletes
      invalidate affected nodes and coalesced neighbors, not a global tail) —
      CM-RPL-002
- [x] Incremental-render equivalence oracle: property/fuzz test asserting
      byte-equality with full re-render over event sequences including edits and
      deletes — CM-RPL-002
- [x] `ListSessionEventsBySession` pagination/keyset; replay excludes what the
      history checkpoint stands for before loading payloads — CM-RPL-001
- [x] `ListHistoryMessagesAfterWithinBytes` / `ListHistoryTurnsByBot`: LIMIT or
      token-budgeted incremental loading — CM-RPL-001
- [x] Metrics: replay event count/bytes, cache eviction counters — CM-OBS-001

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

- [ ] 5,000 events: no OOM; bounded reads and the hard boundary engage
- [ ] Request over budget: one synchronous compaction before the model call;
      summarizer failure / no reduction / still too large →
      `context.budget_unsatisfied` with no provider call
- [ ] `context_window = 0` or grossly mismatched: absolute cap engages
- [ ] Message edits after a fold preserved; new messages that arrive during
      compaction stay after the checkpoint it commits
- [ ] Production-scale regression (8,000 history messages / 5,000 events / no
      summaries): record the pre-fix baseline peak, then assert (a) **decoupling** —
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

Package tests cover: admission arithmetic and fail-closed decisions, estimator
convergence, checkpoint-first replay, cache eviction, incremental accounting equality
with full serialization (shadow comparison), and copy-on-write snapshot semantics.
They complement but never replace black-box acceptance.

## 9. Acceptance scenarios

| Scenario | Setup | Primary assertion | Requirements |
|---|---|---|---|
| oversized cold session | 8k messages, 5k events, no summaries | Bounded reads; the run compacts once and answers, or fails with a stable code; provider request ≤ budget; peak RSS bounded | CM-ADM-001/002, CM-PRC-001 |
| missing context window | model with `context_window = 0` | Absolute cap enforced; no unbounded provider call | CM-ADM-001 |
| source outage | checkpoint or history load fails on long thread | Request fails with a retryable error; no recomposition, no smaller context; input unconsumed | CM-ADM-003 |
| truncated load | history after the checkpoint exceeds the load's byte budget | The rows the load left out fold before the request is built, and the request answers or fails with a stable code; no request renders history that starts later than its checkpoint | CM-ADM-003, CM-CMP-001 |
| compaction | request over budget | One compaction before the model call; rebuilt context in budget; no second compaction for the same request | CM-CMP-001 |
| summary density | histories of different densities | Summary length follows the content; a valid summary within the stated bound continues without another compaction | CM-CMP-001 |
| compaction failure | summarizer error / no reduction / still too large / unreadable item | `context.budget_unsatisfied`; no provider call; input unconsumed; a committed summary stays, nothing else changes | CM-CMP-001, CM-ADM-002 |
| compaction latency | request over budget | Outcome, passes, and duration recorded per compaction | CM-CMP-003 |
| long tool loop | in-run growth past budget, from web, IM, a queued follow-up, a decision continuation, a schedule fire, a retry, an edit, and Discuss | The run folds eligible sources into one rolling summary and continues; requests between events only grow, every tool result reaches the provider as stored, and each effect runs once; the next turn reads the summary back | CM-CMP-001 |
| full prefix, first step | history before the run fills the request after one step | The run folds that history at the first boundary and continues | CM-CMP-001 |
| long tool loop, fold cannot help | summarizer unavailable, or kept rows leave no room for a summary | `context.budget_unsatisfied` (or `context.protected_overflow`); committed steps stay, nothing is replayed; the next turn can recover before dispatch | CM-CMP-001, CM-ADM-002 |
| replaced compacted turn | retry or edit of a turn that compacted in place | The attempt folds the history before the cutoff into a summary of its own; the active checkpoint stands for no row of the replaced turn | CM-CMP-001 |
| Discuss run fails after a fold | a message arrives during the run, then the summarizer stops answering; the worker restarts with no durable cursor | The arrival is not covered by the run's summary; committed steps stay once; after the restart one answer carries the failed input and the arrival | CM-CMP-001, CM-ADM-002 |
| long tool loop outside the set | subagent | No in-run fold; requests only grow, and the first step request that does not fit ends with `context.budget_unsatisfied` without replaying an effect | CM-CMP-001, CM-ADM-002 |
| ACP long thread | Discuss ACP, oversized history | Prompt built within budget; no unbounded `Query` | CM-ADM-001 |
| replay at scale | cold start with 5k events | Paginated loads; what the checkpoint stands for is skipped before its payloads load; bounded replay memory | CM-RPL-001/002 |
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
