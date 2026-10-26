# Subscription model limits

Model limits belong to the subscription's catalog, not to the model name alone.
Do not copy the ordinary API's limits onto a same-named subscription model.

| Subscription | Context window | Maximum output | Discovery |
|---|---|---|---|
| OpenCode Go | `opencode-go` catalog `limit.context` | `limit.output` | Server-owned models.dev catalog import |
| GitHub Copilot | Account `/models` `capabilities.limits.max_context_window_tokens` | `capabilities.limits.max_output_tokens` | Authenticated account catalog |
| OpenAI Codex | Positive `context_window`, falling back to positive `max_context_window` only when the default is absent or invalid | Unknown when the catalog does not declare it | Authenticated `/codex/models` |

Copilot's prompt limit and output limit are independent of its context window;
do not add them together to derive the window. Missing or nonpositive limits
stay unknown, so refreshing does not replace an existing valid limit with zero.

Codex's `truncation_policy.limit` controls tool output truncation, and
`auto_compact_token_limit` controls when its own agent compacts. Neither is the
model's maximum output. Memoh's `openai-codex` transport does not enforce an
output cap; setting `max_output_tokens` in the database does not change that.
It remains ineligible for native automatic summarization that requires a hard
output cap. This change does not remove that guard or claim to solve those
compaction failures; they require a separate summarizer/transport design.

## Existing installations

After upgrading the server, refresh the affected provider's models:

- OpenCode Go: use the catalog refresh, not the optional live endpoint listing.
  Its public `/models` response can list IDs without any limits. Legacy official
  subscriptions without a catalog source are recognized on provider reads, so
  Web/Desktop select the catalog import and the backend accepts the same source.
  An existing client that explicitly calls `/import-models` still requests the
  endpoint list; use `/import-catalog-models` to repair those limits.
- Codex and Copilot: refresh the authenticated account catalog. They must not
  inherit a public catalog's availability or limits for a different account.

Refresh updates existing model rows in place and leaves enable/name/description
choices in place; new models are disabled. Provider reads and public catalog
cache refreshes do not rewrite saved model configurations. No migration or
background full-table backfill is introduced. A custom proxy needs an explicit,
verified catalog source or manually verified limits; protocol alone is not enough.

## Evidence

- [OpenCode Go integration](https://opencode.ai/v2/docs/console/go) and
  [public Go model list](https://opencode.ai/zen/go/v1/models).
- [models.dev catalog](https://models.dev/api.json), scoped to `opencode-go`.
- [GitHub Copilot SDK model limit types](https://github.com/github/copilot-sdk/blob/main/go/types.go).
- [Official Codex model catalog](https://github.com/openai/codex/blob/main/codex-rs/models-manager/models.json)
  and [model metadata definitions](https://github.com/openai/codex/blob/main/codex-rs/protocol/src/openai_models.rs).

Tests use local catalog fixtures; they do not send generation requests or use
production subscription credentials.
