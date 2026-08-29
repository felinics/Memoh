# Remote ACP design

## Scope

Remote ACP is a process and stdio bridge. Memoh Server starts a fixed ACP
adapter on a connected computer and exchanges ACP JSON-RPC over that process's
stdin and stdout.

Remote ACP does not manage Codex or Claude Code credentials. It does not copy,
stage, rewrite, synchronize, or delete either tool's local configuration. It
does not inject provider API keys or decide which login method the local tool
uses. The adapter and local CLI run with the connected computer's own local
setup.

## Data path

```text
Memoh Server (ACP client)
        │
        │ bridgepb.ContainerService.Exec (bidirectional gRPC stream)
        │
existing outbound WebSocket / HTTP2 gRPC connection
        │
@memohai/runtime
        │
fixed private launcher alias
        │
bundled codex-acp or claude-agent-acp
        │
user-installed local Codex or Claude Code CLI
```

The computer opens one outbound WebSocket connection to Server. gRPC uses that
connection as an HTTP/2 transport. Every ACP adapter process gets its own
`Exec` stream on that connection:

- the first client message contains command, working directory, and process
  options;
- later client messages contain stdin bytes;
- server messages are explicitly tagged stdout, stderr, or exit;
- cancelling or closing one stream terminates only that stream's supervised
  process;
- closing the computer connection terminates all processes owned by that
  connection.

HTTP/2 multiplexing keeps stream flow control and message ordering independent,
so concurrent ACP sessions do not share stdin or stdout state and cannot splice
their messages together. No ACP-specific network protocol or second connection
is required.

## Trust model

Remote ACP inherits the trust model of the Runtime's existing `exec`
capability: a connected computer already lets Memoh Server run arbitrary
shell commands in any directory as the connecting user. The fixed launcher
mechanism is name resolution and path hygiene, not confinement — it
guarantees that the aliases `codex-acp` and `claude-agent-acp` resolve to the
bundled adapters and that local CLI paths never leave the computer, but it
does not reduce what a compromised or malicious Server could execute.
Connecting a computer is therefore an act of full trust in the Server, and
the UI must present it that way. A future capability-scoped Exec mode
(allowlist of the two launcher aliases, cwd restricted to approved Folders)
would be required before Remote ACP could be granted without general shell
access; no such mode exists today.

## Responsibilities

Memoh Server remains the ACP client. It owns session routing, prompts, ACP
message parsing, approvals, runtime ownership, and the immutable Computer/Folder
binding. It never receives a local adapter path or local CLI path.

`@memohai/runtime` owns the local gRPC service, safe process environment,
private launcher aliases, child-process supervision, and byte forwarding. It
advertises `acp_codex` or `acp_claude_code` only when the corresponding local
launcher can be constructed.

Desktop bundles exact JavaScript adapter versions and locates the user's local
`codex` or `claude` entry in Electron Main. These paths are passed directly to
Runtime through a typed in-process descriptor; they are not exposed to the
renderer or Server. The native Codex and Claude Code CLIs are not bundled.

The CLI connection command installs `@memohai/runtime`, `codex-acp`, and
`claude-agent-acp` into the same temporary npm execution environment. Their bin
entries therefore appear on the Runtime process PATH without a separate global
installation.

## Workspace and lifecycle

The Session's persisted Folder fixes the remote target and absolute workdir.
Server re-resolves that target for each turn and never silently falls back to a
different Primary computer. Removing a target that is still referenced by a
Folder is rejected.

Remote adapters currently run on macOS and Linux. A disconnected Runtime or a
Server restart starts a new ACP process and native ACP session; Memoh chat
history remains durable, but native ACP session resume is a separate feature.

The Runtime connection key authenticates the computer to Memoh. It is transport
authentication, not a Codex or Claude Code credential.

## Core verification

The implementation keeps focused coverage for:

- capability detection and fixed launcher construction;
- adapter startup with the local CLI path kept inside Desktop/Runtime;
- bidirectional Exec stream stdin/stdout/stderr/exit behavior;
- independent concurrent streams and connection-owned process cleanup;
- no Server-managed Agent environment or state on the remote path;
- immutable workspace target/workdir routing.
