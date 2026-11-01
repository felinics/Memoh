# Memoh Hooks Reference

Use this reference when configuring `/data/.memoh/hooks.json`.

## Table Of Contents

- Config shape
- Matching and priority
- Actions
- Decisions
- Event catalog
- Request payload
- Examples
- Validation checklist

## Config Shape

```json
{
  "version": 1,
  "enabled": true,
  "defaults": {
    "timeout": "10s",
    "on_error": "fail",
    "max_output_bytes": 65536,
    "trigger_nested_hooks": false
  },
  "env": {
    "EXAMPLE": "value"
  },
  "hooks": [
    {
      "name": "log workspace commands",
      "event": "AfterWorkspaceCommand",
      "matcher": ".*",
      "enabled": true,
      "priority": 10,
      "actions": [
        {
          "type": "command",
          "command": "mkdir -p .memoh && cat >> .memoh/hooks.log",
          "timeout": "10s",
          "on_error": "ignore",
          "work_dir": "/data"
        }
      ]
    }
  ]
}
```

Defaults:

- `version`: only `1` is supported.
- `enabled`: defaults to `true`; set `false` to disable user hooks without deleting them.
- `defaults.timeout`: defaults to `10s`; duration strings and positive second integers are accepted.
- `defaults.on_error`: defaults to `fail`.
- `defaults.max_output_bytes`: defaults to `65536`.
- `defaults.trigger_nested_hooks`: defaults to `false`.
- `env`: environment variables added to command actions.
- `hooks`: hook rules.

Hook fields:

- `name`: human-readable label.
- `event`: required event name from the catalog below.
- `matcher`: optional regular expression matched against the event's target text.
- `enabled`: defaults to `true`.
- `priority`: higher numbers run first.
- `conditions`: reserved field; current runtime does not evaluate it, so do not rely on it.
- `actions`: command or tool actions to run when the hook matches.

## Matching And Priority

The matcher target is selected in this order:

1. `tool.name`
2. `approval.tool_name`
3. `channel.platform`
4. `memory.scope`
5. `extra.command`
6. `extra.path`
7. `extra.operation`
8. `extra.scope`
9. event name

Use `matcher` to narrow a hook. Examples:

- `"^exec$"`: only the `exec` tool.
- `"^(rm|sudo|curl)\\b"`: commands beginning with risky shell words.
- `"\\.env$"`: paths ending in `.env`.
- `"before_chat"`: memory search scope.

If multiple hooks match the same event, higher `priority` runs first. Hooks with the same priority keep config order.

## Actions

### command

Runs a shell command in the workspace. The hook request JSON is written to stdin.

```json
{
  "type": "command",
  "command": "python3 .memoh/hooks/check.py",
  "work_dir": "/data",
  "timeout": "10s",
  "on_error": "block"
}
```

Command action details:

- `command` is required.
- `work_dir` defaults to `request.workspace.cwd`, then `/data`.
- `timeout` defaults from `defaults.timeout`.
- `on_error` defaults from `defaults.on_error`.
- Environment includes `env` plus `MEMOH_HOOK_EVENT`, `MEMOH_HOOK_NAME`, `MEMOH_BOT_ID`, and `MEMOH_SESSION_ID`.
- Stdout is parsed as JSON only if the entire trimmed stdout is valid JSON. Otherwise the action is treated as `allow` and raw stdout is stored in metadata.

### tool

Calls a Memoh tool by name when a hook tool runner is available for that event.

```json
{
  "type": "tool",
  "tool": "send_message",
  "input": {
    "text": "Hook fired"
  },
  "on_error": "ignore"
}
```

Tool action details:

- `tool` is required.
- `input` is optional and defaults to `{}`.
- Tool output may include `decision`, `reason`, or `append_context`.
- Some hook events do not provide a tool runner; use `command` for portable hooks.

### reserved

`mcp_tool` is reserved for a later version and currently fails validation.

## Decisions

Actions may return:

```json
{
  "decision": "allow",
  "reason": "optional explanation",
  "append_context": "optional context",
  "metadata": {
    "source": "hook"
  }
}
```

Valid decisions:

- `allow`: continue normally.
- `deny`: block the guarded operation.
- `ask_approval`: force an approval flow where the caller supports approvals, especially `PreToolUse`.
- `append_context`: append `append_context` to the model or resolver context for events that consume it.

Unknown or empty decisions normalize to `allow`.

`on_error` controls action failures:

- `ignore`: log and continue.
- `fail`: return the action error.
- `block`: convert the error into a deny decision.

## Event Catalog

Agent/tool events:

- `PreToolUse`: before a tool approval decision; `tool.name`, `tool.call_id`, `tool.input`.
- `PostToolUse`: after a tool succeeds; `tool.name`, `tool.call_id`, `tool.input`, `tool.result`.
- `ToolError`: after a tool returns an error; `tool.name`, `tool.call_id`, `tool.input`, `tool.error`, `error`.
- `BeforeModelCall`: before a model call; `turn.session_type`, `turn.model`, `turn.step`, `turn.message_count`. Supports `append_context`.
- `AfterModelCall`: after a model call; model/token/tool count fields in `turn`.
- `TurnEnd`: after a successful turn.
- `TurnError`: after an aborted or failed turn; includes `error`.

Conversation and prompt events:

- `SessionStart`: after session creation; `turn.session_type`, `turn.route_id`, `turn.channel_type`.
- `UserMessageReceived`: after inbound user message normalization; `channel.platform`, `channel.conversation_type`, `extra.query`, `extra.raw_query`, `extra.attachment_count`. Supports `append_context`.
- `BeforePromptBuild`: before system prompt assembly; `turn.session_type`, `turn.message_count`. Supports `append_context`.
- `AfterPromptBuild`: after system prompt assembly; `turn.system_bytes`. Supports `append_context`.

Memory events:

- `BeforeMemorySearch`: before memory context lookup; `memory.scope`, `memory.query`.
- `AfterMemorySearch`: after memory lookup; `memory.scope`, `memory.query`, `memory.result_count`, optional `memory.context_bytes`. Supports `append_context`.
- `BeforeMemoryWrite`: before writing chat memory; `memory.scope`, `memory.message_count`.
- `AfterMemoryWrite`: after writing chat memory; `memory.scope`, `memory.message_count`.
- `MemoryExtracted`: after memory extraction; `memory.scope`, `memory.message_count`.

Workspace events:

- `WorkspaceStart`: after workspace start; `extra.backend`, `extra.image`, optional `extra.path`.
- `WorkspaceStop`: before workspace stop; `extra.timeout_seconds`.
- `BeforeWorkspaceCommand`: before an exec command; `extra.command`, `extra.work_dir`, `extra.timeout_seconds`, `extra.run_in_background`.
- `AfterWorkspaceCommand`: after an exec command; same fields plus optional `extra.error`.
- `BeforeFileWrite`: before write/apply_patch; `extra.path` and `extra.bytes`, or `extra.operation`, `extra.summary`, `extra.files`.
- `AfterFileWrite`: after write/apply_patch; same fields as before write.

Approval events:

- `BeforeApprovalCreate`: before creating a pending approval; `approval.tool_name`, `approval.tool_call_id`, `approval.operation`, `approval.tool_input`. Can deny creation.
- `ApprovalRequested`: after an approval request is created.
- `ApprovalResolved`: after approval or rejection.
- `ApprovalTimeout`: after approval timeout notification.

Compaction and subagent events:

- `PreCompact`: before compaction; `turn.input_tokens`, `turn.history_budget_tokens`, `turn.model_id`.
- `PostCompact`: after compaction; same fields plus optional `turn.error`.
- `SubagentStart`: before a subagent task; `extra.agent_id`, `extra.agent_session_id`, `extra.task_id`, `extra.message`.
- `SubagentStop`: after a subagent task; same fields plus optional `extra.status`, `extra.error`, `extra.text_bytes`.

Declared but not currently runtime-emitted in the main hook runtime:

- `InboundMessageNormalized`
- `BeforeOutboundMessage`
- `AfterOutboundMessage`
- `ChannelDeliveryFailed`

## Request Payload

Hook commands receive JSON like:

```json
{
  "version": 1,
  "event": "BeforeWorkspaceCommand",
  "hook_name": "guard shell",
  "bot_id": "bot-id",
  "session_id": "session-id",
  "chat_id": "chat-id",
  "workspace": {
    "cwd": "/data",
    "runtime": "container"
  },
  "tool": {
    "name": "exec",
    "call_id": "call-id",
    "input": {}
  },
  "approval": {},
  "turn": {},
  "memory": {},
  "channel": {},
  "extra": {
    "command": "npm test"
  },
  "error": ""
}
```

Not every event sets every object. Scripts should tolerate missing fields.

## Examples

### Log every workspace command

```json
{
  "name": "log workspace commands",
  "event": "AfterWorkspaceCommand",
  "matcher": ".*",
  "priority": 10,
  "actions": [
    {
      "type": "command",
      "command": "mkdir -p .memoh && cat >> .memoh/workspace-commands.jsonl",
      "on_error": "ignore"
    }
  ]
}
```

Because the request JSON is stdin, `cat >> file` records one JSON object per line.

### Block writes to env files

```json
{
  "name": "protect env files",
  "event": "BeforeFileWrite",
  "matcher": "\\.env(\\..*)?$",
  "priority": 100,
  "actions": [
    {
      "type": "command",
      "command": "python3 -c 'import json,sys; req=json.load(sys.stdin); print(json.dumps({\"decision\":\"deny\",\"reason\":\"env files are protected\"}))'",
      "on_error": "block"
    }
  ]
}
```

### Ask approval before risky exec commands

```json
{
  "name": "approval for risky shell",
  "event": "BeforeWorkspaceCommand",
  "matcher": "^(rm|sudo|curl|wget)\\b",
  "priority": 100,
  "actions": [
    {
      "type": "command",
      "command": "python3 -c 'import json; print(json.dumps({\"decision\":\"ask_approval\",\"reason\":\"risky shell command\"}))'",
      "on_error": "block"
    }
  ]
}
```

### Add context before model call

```json
{
  "name": "add local reminder",
  "event": "BeforeModelCall",
  "priority": 0,
  "actions": [
    {
      "type": "command",
      "command": "python3 -c 'import json; print(json.dumps({\"decision\":\"append_context\",\"append_context\":\"Remember to check AGENTS.md before editing.\"}))'",
      "on_error": "ignore"
    }
  ]
}
```

## Validation Checklist

- The file is valid JSON.
- `version` is `1`.
- Every hook has a supported `event`.
- Every `matcher` compiles as a regular expression.
- Every action has type `command` or `tool`.
- Every command action has `command`.
- Every tool action has `tool`.
- `on_error` is `ignore`, `fail`, or `block`.
- Guardrail hooks use narrow matchers and clear reasons.
- Logging hooks use `on_error: "ignore"`.
