# OpenCode Go integration

Memoh supports OpenCode Go as the `opencode-go` client type. Select **OpenCode Go**
under Settings → Providers, enter a Go API key, enable the provider, and import
and enable the desired models. The default base URL is
`https://opencode.ai/zen/go/v1`.

## Dependency and ownership

Twilight ([PR #49](https://github.com/felinics/twilight/pull/49)) owns the
model-to-protocol routing, request header support, and HTTP wire formats. Like
OpenCode itself, it sends every model to Chat Completions except a small table
of models documented on Responses or Anthropic Messages. Unlisted models use
the Completions fallback; new protocol exceptions still require an SDK update.
Its Go provider also adapts requests to how
Go's routes behave, such as padding `reasoning_content` on replayed tool calls,
so Memoh sends every request through it. Memoh reads the routed protocol from
`ProtocolForModel` for prompt caching and media handling. Reasoning policy keeps
the Go provider identity and uses each model's catalog-declared controls. The
Go adapter applies those controls before sending, preserving native tiers such
as `max` and explicit thinking switches without inferring Claude's adaptive mode.

Memoh owns `x-opencode-session`: native turns use the owning Thread ID, including
subagent turns; title generation and compaction use that same conversation ID.
Standalone jobs and model probes receive independent job IDs. Request contexts
carry the session without changing shared provider headers. Requests identify
the application with Memoh's normal User-Agent.

## Catalog and migration

`conf/providers/opencode-go.yaml` contains the models in the
[Go endpoint table](https://opencode.ai/docs/go/#endpoints) except time-limited
free models, with context windows and input capabilities from
[models.dev](https://models.dev/api.json), checked on 2026-09-30. Both Luna models
expose off/low/medium/high/xhigh/max. Other entries declare their supported native
tiers, a thinking switch, or a token budget. Qwen 3.7's low/medium/high choices
use Memoh's existing 5,000/16,000/50,000-token budget allowances, bounded by the
catalog; they are not native effort tiers. Models without verified controls use
provider-managed reasoning and show no manual reasoning control in the picker.
Manual custom providers have
conservative text/tool/reasoning discovery defaults; the preset supplies richer
catalog metadata.

Server startup synchronizes the bundled catalog into provider templates. Use
**Refresh Models** on an existing provider to apply those capabilities to its
stored models. The live endpoint determines which models are available, while
the curated catalog supplies the controls missing from its model-list response.
This integration does not periodically fetch models.dev.

Migration `0160_opencode_go` extends the provider type constraint. The canonical
initial schema includes the same type. Rollback refuses to proceed while Go
providers exist, because converting a provider with mixed protocols to a single
legacy type would break its models. Remove those configurations before downgrading.

## Follow-up

- When Go documents a new Responses or Messages model, add it to Twilight's
  exception table and to the preset together. Do not infer protocols from model
  name prefixes.
- Update catalog capabilities when Go adds or changes models, after verifying
  their wire contracts. Unlisted models retain provider-managed reasoning until
  controls are declared.

Automated coverage includes all three wire paths for generation and streaming,
every preset model's selectable reasoning controls, reasoning/cache decoration,
tool continuation session stability, concurrent
conversation isolation, native parent/child session ownership, standalone probes,
and the Completions default for unlisted models. A public model-list connectivity check
does not verify credentials; use **Test Model** and a real chat for that purpose.
