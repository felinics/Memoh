# Session Runtime acceptance tests

This package verifies the public Session Runtime contract defined in
`docs/design/session-runtime-requirements.md`.

The tests deliberately use only:

- public HTTP APIs;
- the public WebSocket chat endpoint;
- real `cmd/agent` Server processes;
- a real PostgreSQL database shared by both Server instances;
- an in-process OpenAI-compatible fake model exposed to the containers.

They must not construct `session.Manager`, a handler, or a database adapter
directly. Internal algorithms remain covered by
`internal/agent/runtime/session/*_test.go`.

The Compose topology gives each Server an independent containerd state. During
fixture setup the suite calls the public `POST /bots/{id}/container/start`
endpoint on `server_b`, so the test Bot has a usable workspace on both
instances. This setup step is necessary to keep workspace availability from
masking Session Runtime behavior.

## Start the environment

The acceptance override isolates PostgreSQL, pgvector, and container state from
the normal development volumes. It also starts `server_b` on port `18083`.

```bash
mise run test:session-runtime:acceptance:env
```

This command re-creates the development service containers with the acceptance
volumes and waits for both Server instances to become healthy. It does not
delete the normal development volumes.

## Run

```bash
mise run test:session-runtime:acceptance
```

The direct equivalent is:

```bash
MEMOH_SESSION_RUNTIME_ACCEPTANCE=1 \
MEMOH_SESSION_RUNTIME_ACCEPTANCE_REQUIRED=1 \
go test -tags=integration -count=1 -timeout=5m \
  ./internal/agent/runtime/session/acceptance
```

Useful environment variables:

| Variable | Default | Purpose |
| --- | --- | --- |
| `MEMOH_SESSION_RUNTIME_PRIMARY_URL` | `http://127.0.0.1:18080` | Primary Server API |
| `MEMOH_SESSION_RUNTIME_SECONDARY_URL` | `http://127.0.0.1:18083` | Secondary Server API |
| `MEMOH_SESSION_RUNTIME_USERNAME` | `admin` | Acceptance account |
| `MEMOH_SESSION_RUNTIME_PASSWORD` | `admin123` | Acceptance password |
| `MEMOH_SESSION_RUNTIME_FAKE_MODEL_PORT` | `19090` | Stable host port used by both Server instances |
| `MEMOH_SESSION_RUNTIME_ACCEPTANCE_REQUIRED` | unset | Fail instead of skip when the environment is unavailable |
| `MEMOH_SESSION_RUNTIME_ACCEPTANCE_CRASH` | unset | Enable the test that kills and restarts the primary Server container |
| `MEMOH_SESSION_RUNTIME_PRIMARY_CONTAINER` | `memoh-dev-server` | Container used by the crash test |

The crash test is opt-in because it intentionally sends `SIGKILL` to the
primary Server. Use it only with the acceptance environment:

```bash
MEMOH_SESSION_RUNTIME_ACCEPTANCE_CRASH=1 \
mise run test:session-runtime:acceptance
```

## Expected result on the initial main baseline

Observed on `main@b49939582`:

| Requirement | Result | Observation |
| --- | --- | --- |
| `SR-BASE-001` | pass | One model execution; one user turn and one assistant turn persisted |
| `SR-OBS-001` | fail | `runtime_subscribe` is rejected as an unknown message type |
| `SR-CTL-001` | fail | Secondary Server returns no control ack and does not cancel the owner |
| `SR-ADM-001` | fail | One invocation executes twice and persists two user turns |
| `SR-OWN-001` | fail | Both Server instances enter the model concurrently (`max_active=2`) |
| `SR-DUR-001` | fail | After `SIGKILL`, the accepted input is absent from history and no run snapshot exists |

The first five cases run in the normal task. `SR-DUR-001` remains opt-in
because it restarts the primary container.
