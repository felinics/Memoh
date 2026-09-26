# Agent lifecycle QA — 2026-09-26

## Scope and environment

The model was a deterministic local fixture (`scripts/qa/agent-lifecycle-model.py`).
The application UI, tools, command processes, PostgreSQL ledger, Valkey leases,
WebSocket traffic, and Server container replacement were real. No virtual clock
was used for the long-task or rolling scenarios.

- UI: `http://localhost:19092`; proxy health endpoint: `http://localhost:19090/ping`.
- Base development stack: documented `mise run dev`, isolated `memoh-timeout-qa`.
- Rolling replicas used a separate `memoh_rolling_qa` database cloned from that
  task-owned fixture database, a separate Valkey instance, and an Nginx proxy.
- Workspace/containerd remained on an independent workspace host. Server replicas
  shared its workspace volume and accessed containerd through a Unix socket relay.
  Replicas used the development containerd backend's required host PID namespace
  and privileges. This topology tests Server ownership transfer while keeping the
  workspace alive; it is not a Cloud E2B or production Helm deployment.
- Replica A ran the previous PR head `62a27c494`; B ran the tenant recovery change.
  A second replacement (C to D) used the final HTTP-drain correction on both nodes.

## Tenant and admission checks

`TestPostgresResumeScopesUseOrdinaryRoleAndPreserveTenantIntoExecution` creates two
Teams and an ordinary `NOSUPERUSER NOBYPASSRLS` role. Reads use one physical pooled
connection so switching the bound Team is exercised. The test verifies forced RLS,
only one Team's row visible, paginated discovery of both Teams, correct context in
the continuation, and rejection of an unbound query. Fixture creation/cleanup uses
the test administrator; the discovery queries use the ordinary role.

`TestPostgresResumeRejectsCandidateSupersededAfterScan` finishes a newer user turn
between discovery and recovery admission. The old continuation is rejected under
the admission lock without inserting another run. The existing eight-contender
PostgreSQL test also verifies that only one continuation executes.

These exercise the OSS scope-provider contract. A hosted composition root must
still supply its own scope catalog/binder and credential/runtime adapters.

## Real long task

Run the fixture with `--long-seconds 660`, then send this through the UI:

```text
QA_LONG_PARENT: run the real 660-second child command, wait for it, and report the outcome.
```

The fixture requests a synchronous subagent. That child starts a real finite
workspace command, emits progress every 30 seconds, and waits through repeated
`wait_until` calls. The command has a 900-second budget.

| Run | Result | Ledger duration |
| --- | --- | --- |
| Parent `93af3716-e660-44a2-8919-2473797b33df` | completed | 660.221535 seconds |
| Child `34abb3b2-27fb-4ba6-83fc-4480aeae5d8a` | completed | 660.134352 seconds |

Both were still running at 634 seconds. The UI then showed `LONG_CHILD_COMPLETE`
and `LONG_PARENT_COMPLETE`. The workspace receipt contained one `long-start` and
one `long-done`. This validates the existing stack's removal of the ten-minute
subagent wall-clock limit on the pre-upgrade replica.

## Rolling replacement

1. Keep the old replica serving traffic and start the new replica with the same
   PostgreSQL and Valkey configuration (`backend="redis"`, `cluster=true`).
2. Send `QA_ROLL:` through the UI. Its real command appends one marker to a file;
   the following model step remains streaming.
3. Confirm the new replica is ready. Change the proxy upstream, reload it, and
   confirm `X-QA-Upstream` identifies the new replica before stopping the old one.
4. Stop the old Server container with a 45-second grace period. Do not send another
   user message. Inspect the ledger, UI, and marker file on the new replica.

The first A-to-B run resumed successfully, but exposed an HTTP drain problem:
long-lived requests held shutdown until the 30-second deadline. An immediate proxy
reload/stop also produced one HTTP 502 among 200 health probes. The final correction
cancels HTTP request contexts after saving interruption markers, allowing SSE
handlers to exit before their hubs' later cleanup hooks. The final procedure also
confirms the proxy's new upstream before stopping the old replica.

Final C-to-D result:

| Item | Observation |
| --- | --- |
| Source run | `14b2bfb2-cd6c-4923-ba4b-27389bbefade`, lost / `session_runtime.interrupted` |
| Continuation | `aa9fcd11-638a-4717-a7b5-5e48772b346d`, completed |
| Ownership | changed from C to D; fencing token 26 → 27 |
| Old container exit | code 0; `docker stop` took approximately 0.122 seconds |
| Proxy probes | 200/200 HTTP 200 over 42.18 seconds |
| Command receipt | one line before and after replacement |
| UI | `ROLL_RESUMED` appeared without another user message |

This bounded health probe is not a general zero-downtime guarantee. Kubernetes
endpoint propagation, Cloud authentication, real provider CLIs, E2B command
adoption, and ungraceful node failure require their own downstream acceptance.

## Automated validation

- Full Go suite passed after resolving the new default-Team architectural guard.
  One unrelated subscriber-overflow timing test failed during concurrent heavy
  validation; its isolated rerun and the subsequent full suite passed.
- Real PostgreSQL tenant/admission tests and focused race tests passed.
- A real HTTP streaming test verifies shutdown cancels its request context and
  drains before the deadline; relevant application/server/runtime tests passed.
- Generated SQL code was regenerated with `mise run sqlc-generate`; no migration
  was added by this change.

Screenshots and sanitized ledger receipts are linked in PR #1391. All browser
interactions were agent-operated; Human QA remains unconfirmed.
