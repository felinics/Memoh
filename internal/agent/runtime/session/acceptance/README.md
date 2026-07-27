# Session Runtime acceptance tests

This package verifies the public Session Runtime contract in
`docs/design/session-runtime-requirements.md` against the current durable
`session_runs` schema.

The tests drive public HTTP and WebSocket APIs plus the authenticated Turn gRPC
process boundary used by a standalone Channel service. They do not construct a
`session.Manager`, handler, application service, sqlc query set, or database
adapter. A read-only SQL probe observes the deployment database after public
operations so the suite can verify facts that must survive process loss:

- one `session_id + invocation_id` maps to one `run_id` and `turn_id`;
- busy submissions leave no `session_runs` row and do not advance
  `bot_sessions.next_turn_position`;
- no session has more than one active ledger row;
- terminal history rows carry the admitted `run_id` and `turn_id`;
- pending decisions carry the same run and turn identities;
- owner or live-backend loss converges to a durable `lost` state.

Package-level algorithms remain covered by
`internal/agent/runtime/session/*_test.go`.

## Topologies

The suite has two isolated deployment shapes:

| Topology | Servers | Live backend | Purpose |
| --- | ---: | --- | --- |
| `single` | 1 | memory | Default OSS boundary; Redis must not be required |
| `cluster` | 2 | Valkey | Cross-instance ownership, subscriptions, control and loss handling |

Both use real PostgreSQL 18 and a controllable OpenAI-compatible fake model.
The cluster topology uses a test-only TOML config and a non-persistent Valkey on
host port `16379`. Normal development and production configs are not modified.

Start one topology, run it, then stop it before switching:

```bash
mise run test:session-runtime:acceptance:env:single
mise run test:session-runtime:acceptance:single
mise run test:session-runtime:acceptance:env:down
```

```bash
mise run test:session-runtime:acceptance:env:cluster
mise run test:session-runtime:acceptance:cluster
mise run test:session-runtime:acceptance:cluster:destructive
mise run test:session-runtime:acceptance:env:down
```

The compatibility aliases `test:session-runtime:acceptance:env` and
`test:session-runtime:acceptance` select the cluster topology.

## Coverage

| Requirement | Black-box assertion |
| --- | --- |
| `SR-BASE-001` | `run_accepted`, ordered runtime output, completed ledger row, run-linked user and assistant history |
| `SR-ADM-001` | concurrent duplicate submissions return the same run/turn; fingerprint conflicts are stable and create no second row |
| `SR-OWN-001` | a second invocation receives `session_busy`, writes nothing, does not advance the turn counter, then succeeds after the active run ends |
| `SR-OBS-001` | reconnecting through the other Server receives a snapshot matching the durable run and turn |
| `SR-OBS-003` | two subscribers converge; closing the initiating socket does not stop the observer or run |
| `SR-CTL-001` | abort through the non-owner Server returns `control_ack`, cancels the model call and commits `aborted` |
| `SR-DUR-001` | after `SIGKILL`, input and identity remain in `session_runs` and the run becomes queryable as `lost` |
| `SR-DUR-002` | replaying a completed invocation does not call the model or add history rows |
| `SR-DEC-001` | live `ask_user` and tool approval decisions retain one run/turn/fence across WebSocket, HTTP and Turn gRPC; owner restart preserves and resumes the same decision |
| `SR-CTL-001` decision replay | a decision `control_id` returns the same acknowledgement after the run terminalizes; a stale new control resolves as `applied=false` without a retry code |
| PostgreSQL write budget | several owner-lease renewals leave `session_runs.updated_at`, owner, token and generation unchanged |
| backend loss | `FLUSHDB` preserves the grace interval and admission gate; old-generation runs become `lost` without replaying partial text; new-generation runs complete |

The fake model supports deterministic chunk count and pacing plus test-only
blocking and decision modes:

```text
[acceptance:marker chunks=20 delay_ms=50]
[acceptance:marker mode=block]
[acceptance:marker chunks=3 mode=partial_block]
[acceptance:marker mode=ask_user]
[acceptance:marker mode=tool_approval]
```

## Direct invocation

```bash
MEMOH_SESSION_RUNTIME_ACCEPTANCE=1 \
MEMOH_SESSION_RUNTIME_ACCEPTANCE_REQUIRED=1 \
MEMOH_SESSION_RUNTIME_ACCEPTANCE_MODE=cluster \
MEMOH_SESSION_RUNTIME_ACCEPTANCE_BACKEND_LOSS=1 \
go test -tags=integration -count=1 -timeout=5m \
  ./internal/agent/runtime/session/acceptance
```

Useful environment variables:

| Variable | Default | Purpose |
| --- | --- | --- |
| `MEMOH_SESSION_RUNTIME_PRIMARY_URL` | `http://127.0.0.1:18080` | Primary Server API |
| `MEMOH_SESSION_RUNTIME_SECONDARY_URL` | `http://127.0.0.1:18083` | Secondary Server API |
| `MEMOH_SESSION_RUNTIME_PRIMARY_RPC_TARGET` | `127.0.0.1:19091` | Primary authenticated Turn gRPC endpoint |
| `MEMOH_SESSION_RUNTIME_SECONDARY_RPC_TARGET` | `127.0.0.1:19092` | Secondary authenticated Turn gRPC endpoint |
| `MEMOH_SESSION_RUNTIME_RPC_SECRET` | acceptance config value | Shared secret for the test-only Turn gRPC connection |
| `MEMOH_SESSION_RUNTIME_POSTGRES_URL` | `postgres://memoh:…@127.0.0.1:15432/memoh` | Read-only acceptance probe target |
| `MEMOH_SESSION_RUNTIME_REDIS_URL` | `redis://127.0.0.1:16379/0` | Isolated Valkey used by backend-loss injection |
| `MEMOH_SESSION_RUNTIME_ACCEPTANCE_MODE` | `cluster` | `single` skips two-Server scenarios |
| `MEMOH_SESSION_RUNTIME_ACCEPTANCE_CRASH` | unset | Enable destructive owner/decision restart cases |
| `MEMOH_SESSION_RUNTIME_ACCEPTANCE_BACKEND_LOSS` | unset | Enable `FLUSHDB`; the cluster mise task sets it for isolated Valkey |
| `MEMOH_SESSION_RUNTIME_PRIMARY_CONTAINER` | `memoh-dev-server` | Container killed by crash tests |
| `MEMOH_SESSION_RUNTIME_FAKE_MODEL_PORT` | `19090` | Host port reachable by both Servers |

Crash and backend-loss cases are guarded because they intentionally kill a
container or erase the configured Redis database. The dedicated
`acceptance:cluster:destructive` task runs only the owner-restart contract and
sets the crash guard explicitly. Do not enable either fault against a shared
development or production environment.

## Current implementation boundary

These are target-contract tests, not compatibility tests for the old
per-WebSocket stream registry. Until the production cutover is complete, cases
that require `run_accepted`, `runtime_subscribe`, the durable ledger, or
cross-instance control are expected to fail. A compiling/skipped suite only
proves that the acceptance harness is valid; it does not prove the runtime
contract is implemented.
