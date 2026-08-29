# Remote ACP third-party notices

Memoh Desktop includes these pinned JavaScript adapters:

- `@agentclientprotocol/codex-acp` 1.2.0 — Apache License 2.0
- `@agentclientprotocol/claude-agent-acp` 0.66.0 — Apache License 2.0

Their package license files are retained with the packaged modules. The
adapters' non-optional JavaScript dependencies are also included under their
respective licenses.

`@openai/codex` 0.147.0 is included only as the adapter's JavaScript wrapper;
its platform-native optional packages are excluded. It is Apache-2.0 licensed.
The release package includes the repository's Apache License 2.0 text, its
upstream NOTICE attribution for the 0.147.0 tag, and the npm package
metadata/README. The published npm wrapper itself contains no NOTICE file.

`claude-agent-acp` depends on `@anthropic-ai/claude-agent-sdk` 0.3.220. That
package points distributors to Anthropic's applicable commercial and consumer
terms. A distribution owner must complete its own legal review before shipping
this feature.

Memoh Desktop does not redistribute the native Codex or Claude Code CLI. Users
install those tools separately and remain responsible for their provider
accounts, authentication, local configuration, and terms.
