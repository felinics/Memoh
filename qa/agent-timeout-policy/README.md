# Agent timeout policy QA evidence

Captured from the current `codex/agent-timeout-policy` source in an isolated development stack at http://localhost:19082.

- Schedule form: Chinese, dark mode, desktop and narrow viewport; 90-minute budget saved, reopened, then updated to 120 minutes (API readback: 7200 seconds).
- Managed subagent: parent -> spawn_agent -> child completion -> parent continuation through the real Web UI and backend.
- Repeated scheduled fires: two completed runs in one existing session; distinct persisted fire IDs, one overlapping fire skipped, current_calls=2 and schedule disabled at its configured max_calls.
- Finite command: explicit 2-second execution budget, retained `budget-start` output, unknown outcome when no EXIT frame was received. No automatic replay.
- Service command: running service inspected and explicitly stopped; killed state and original output preserved without an error appended by the late stream completion.

The model endpoint is a deterministic local OpenAI-compatible fixture. Browser interactions and screenshots were performed by an agent. They are not Human QA, real-provider validation, or an hours-long production soak.

Automated checks include Go tests, race tests for affected lifecycle paths, real PostgreSQL ownership/concurrency/recovery tests, migration up/reapply/down/up, and targeted Web tests. See the PR for exact results and baseline lint/typecheck limitations.
