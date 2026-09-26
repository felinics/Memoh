# Agent lifecycle stack QA

All images are fresh screenshots of the running source at http://localhost:19082,
using the documented `mise run dev` command and the isolated memoh-timeout-qa stack.

- bug-split-subagent: no budget migration applied (schema version 156); real parent/child turn completes.
- budget-split-saved: 90-minute budget saved through the Chinese dark UI; PostgreSQL readback 5400 seconds. The migration adds only schedule.max_run_seconds.
- budget-split-command: a real workspace command reaches its 2-second budget; output is preserved and absent exit confirmation is reported as unknown.
- resume-before-restart: a real command has completed, and the next model step is still running.
- resume-after-restart: after Docker restarts the Server container, the continuation completes automatically without another user message. The UI result persists after reload.

The model is a deterministic local OpenAI-compatible fixture; tools, storage,
workspace processes, HTTP/WebSocket interactions and container restart are real.
This is agent-operated QA, not Human QA or a production/provider soak.

Final restart ledger evidence:
0908ef99-75bf-46e1-9b01-5b0fc6eec49a|lost|session_runtime.interrupted|06fe4e0d-55ef-45c2-8e22-d2b58a8ba2a7
833a67c3-1b8f-4f87-8bc8-a67d0b0d31da|completed||resume:0908ef99-75bf-46e1-9b01-5b0fc6eec49a
1 /data/resume-qa-final.log
