# test/steer-disconnect-wait — UI evidence

Local `mise run dev` (branch `test/steer-disconnect-wait`), Web UI at http://localhost:18082,
local OpenAI-compatible mock model streaming one chunk per second.

1. `01-queued-follow-up.png` — the first reply is still streaming; a follow-up is queued.
2. `02-steered-into-running-reply.png` — "Add to current response" interrupts the running
   model invocation: the partial reply is checkpointed and the next call starts 3 s later
   instead of after the full ~12 s stream.
3. `03-steer-final.png` — the continued answer sees one assistant turn (the checkpoint) plus the steer.
