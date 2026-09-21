# OpenCode Go integration

Memoh supports OpenCode Go as the `opencode-go` client type. Select **OpenCode Go**
under Settings → Providers, enter a Go API key, enable the provider, and import
and enable the desired models. The default base URL is
`https://opencode.ai/zen/go/v1`.

## Dependency and ownership

This integration temporarily pins [Twilight PR #49](https://github.com/felinics/twilight/pull/49)
at `479123c22031a9583d473b095dc518367ebe5dbc` through a Go module replacement:

```go
replace github.com/felinics/twilight => github.com/akazwz/twilight-ai v0.4.1-0.20260918160219-479123c22031
```

Twilight owns the model-to-protocol catalog, request header support, HTTP wire
formats, streaming, and tool continuations. Memoh uses `ProtocolForModel` to
construct its existing protocol adapters with the resolved model's thinking
configuration. The effective protocol also controls reasoning options and prompt
caching. Unknown models fail before generation rather than guessing a route;
live discovery excludes models that Twilight cannot route yet.

Memoh owns `x-opencode-session`: native turns use the owning Thread ID, including
subagent turns; title generation and compaction use that same conversation ID.
Standalone jobs and model probes receive independent job IDs. Request contexts
carry the session without changing shared provider headers. Requests identify
the application with Memoh's normal User-Agent.

## Catalog and migration

`conf/providers/opencode-go.yaml` contains the 28 routes in the pinned Twilight
catalog, with context windows and input capabilities from
[models.dev](https://models.dev/api.json), checked on 2026-09-18. Most entries use
provider-managed reasoning defaults; the Luna entry exposes its supported
off/low/medium/high/xhigh controls. The initial integration does not advertise
unverified reasoning controls for other Go models. Manual custom providers have
conservative text/tool/reasoning discovery defaults; the preset supplies richer
catalog metadata.

Migration `0153_opencode_go` extends the provider type constraint. The canonical
initial schema includes the same type. Rollback refuses to proceed while Go
providers exist, because converting a provider with mixed protocols to a single
legacy type would break its models. Remove those configurations before downgrading.

## Follow-up

- Once PR #49 is merged, switch to an upstream revision containing it and remove
  the temporary module replacement. Verify all three protocols again.
- Refresh the Twilight route catalog together with Memoh's model metadata when
  Go publishes or retires models. Do not infer protocols from model name prefixes.
- Expand reasoning controls after verifying each model's Go wire contract.

Automated coverage includes all three wire paths for generation and streaming,
reasoning/cache decoration, tool continuation session stability, concurrent
conversation isolation, native parent/child session ownership, standalone probes,
unknown routes, and discovery filtering. A public model-list connectivity check
does not verify credentials; use **Test Model** and a real chat for that purpose.
