# Schedule Execution Parameters — Design

Date: 2026-08-06
Branch: `feat/schedule-params` (based on `feat/bot-projects` — depends on the workdir domain)

## Goal

Scheduled tasks (cron) gain four execution parameters, configurable both from the
Web schedule tab and by the agent itself through its schedule tools:

1. **Runs in** — a fresh session per fire (current behavior) or an existing session.
2. **Model** — a native provider model, or an ACP agent (e.g. Codex) plus that
   agent's model.
3. **Reasoning effort** — per-schedule override.
4. **Workdir** — bind the schedule's sessions to a bot workdir.

## Decisions (confirmed with user)

- **Existing-session mode**: the target session pins runtime (native/ACP agent)
  and workdir — both non-configurable and inherited. Model + reasoning effort
  remain overridable per schedule.
- **New-session mode**: sessions created by a schedule are **user-visible** —
  they appear in the sidebar and can be continued interactively.
- **Agent tools**: `list_models` gains effort levels; new `list_acp_agents`
  (two-tier: without `agent_id` returns enabled agents instantly; with
  `agent_id` boots a temporary runtime to return models + efforts) and new
  `list_workdirs`. `create_schedule`/`update_schedule` accept the new params.

## Data model

### Migration 0130 — `schedule` columns

```sql
ALTER TABLE schedule
  ADD COLUMN run_target        TEXT NOT NULL DEFAULT 'new_session',
  ADD COLUMN target_session_id UUID REFERENCES bot_sessions(id) ON DELETE SET NULL,
  ADD COLUMN runtime_type      TEXT,           -- NULL = bot default (native model)
  ADD COLUMN acp_agent_id      TEXT,
  ADD COLUMN model_id          UUID REFERENCES models(id) ON DELETE SET NULL,
  ADD COLUMN acp_model_id      TEXT,
  ADD COLUMN reasoning_effort  TEXT,
  ADD COLUMN workdir_id        UUID REFERENCES bot_workdirs(id) ON DELETE SET NULL;
```

Model is two columns on purpose: native models are FK-able UUIDs
(`models.id`), ACP models are agent-reported strings with no backing table.

CHECK constraints:

- `run_target IN ('new_session','existing_session')`
- existing_session ⇒ `target_session_id NOT NULL` and `runtime_type`,
  `acp_agent_id`, `workdir_id` all NULL (inherited from the session).
- new_session ⇒ `target_session_id IS NULL`.
- `runtime_type IS NULL OR runtime_type IN ('model','acp_agent')`
- `runtime_type = 'acp_agent'` ⇒ `acp_agent_id NOT NULL AND model_id IS NULL`
- `runtime_type IS DISTINCT FROM 'acp_agent'` ⇒ `acp_agent_id IS NULL AND acp_model_id IS NULL`
- `NOT (model_id IS NOT NULL AND acp_model_id IS NOT NULL)`

Service-layer validation (DB can't see the target session's runtime):
existing_session + native target session forbids `acp_model_id`; ACP target
session forbids `model_id`. Reasoning effort is validated against
`models.IsValidReasoningEffort`.

### Target session deleted

`ON DELETE SET NULL`. At fire time, `run_target='existing_session'` with a NULL
`target_session_id` writes one error `schedule_log` ("target session deleted")
and **disables the schedule** so it doesn't fail every tick.

### `bot_sessions.visibility` column

`Thread.Visibility` existed in Go but was dead (no consumers; the real gate was
`UserFacingSessionTypes()` filtering on `type`). Make it real:

```sql
ALTER TABLE bot_sessions ADD COLUMN visibility TEXT NOT NULL DEFAULT 'internal'
  CHECK (visibility IN ('user','internal'));
UPDATE bot_sessions SET visibility = 'user' WHERE type IN ('chat','discuss','acp_agent');
```

- Session list endpoints filter on `visibility='user'` by default (explicit
  `types` param still works as a secondary filter).
- Schedule-created sessions in new_session mode are written with
  `visibility='user'` but keep `session_mode='schedule'` so prompt assembly and
  tool gating keep their schedule behavior.
- Historical schedule/subagent sessions stay internal (backfill only
  promotes chat/discuss/acp_agent).

### One fire = one session (new_session mode)

Unchanged semantics: every fire creates a fresh session. UI groups a schedule's
sessions under the schedule rather than introducing a long-lived
schedule-owned session.

## Trigger path

`schedule.Service.runSchedule`:

- **new_session**: create the session with `session_mode='schedule'`,
  `visibility='user'`, the schedule's `workdir_id` (resolved via the workdir
  service like the session-create handler does), and for ACP schedules
  `runtime_type='acp_agent'` + runtime metadata (`acp_agent_id`,
  `project_path` from workdir, `runtime_owner_account_id` = bot owner).
- **existing_session**: reuse `target_session_id`; verify it belongs to the bot
  and is not deleted.

`application.TriggerSchedule` (native path): pass the schedule's
`model_id`/`reasoning_effort` through `ChatRequest.Model`/`ReasoningEffort`
(already supported by `buildBaseRunConfig`).

ACP path: schedules bound to an ACP session (either mode) run the prompt
through the ACP session pool to completion (non-interactive: no user input
requests; tool approvals follow the session's stored policy), persisting the
round like interactive ACP turns do, with per-prompt `ModelID` +
`ReasoningEffort` overrides from the schedule.

## Agent tools

- `create_schedule` / `update_schedule` — new optional params: `session_id`
  (existing-session mode), `model_id`, `acp_agent_id`, `acp_model_id`,
  `reasoning_effort`, `workdir_id`. Defaults = today's behavior.
- `list_models` — include each model's supported effort levels.
- `list_acp_agents` — no args: enabled agents for the bot (fast). With
  `agent_id`: boots a temporary runtime (same path as the web pre-session
  model picker) and returns that agent's models and per-model effort options.
- `list_workdirs` — the bot's workdirs (id, name, target kind, path).

## Web UI (`apps/web` schedule tab)

`schedule-editor.vue` gains an execution section:

- **Runs in**: "New session" / "Existing session" select; the latter shows a
  session picker (user-visible sessions of the bot). Picking a session locks
  runtime + workdir (read-only display).
- **Model**: picker over native models and enabled ACP agents; picking an ACP
  agent loads its models via the pre-session runtime API.
- **Reasoning**: effort select filtered to what the chosen model supports.
- **Workdir**: workdir select (new_session mode only).

Schedule list items surface the configured target (model/agent, workdir) and
link to sessions produced by the schedule.

## Out of scope

- No retention/folding policy for high-frequency schedule sessions beyond
  UI grouping.
