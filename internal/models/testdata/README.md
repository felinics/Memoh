# Model configuration fixtures

These fixed examples were selected from the former provider templates preserved
in commit `a299eb1592bc2d39b316974eb38e280ee2356628`. They are regression inputs,
not a catalog to synchronize with upstream.

- `legacy-model-configs.json` covers empty configs, modalities, declared and
  inferred reasoning modes, effort tiers, disable support and budget boundaries.
- `opencode-go-reasoning.json` covers Completions, Responses and Messages with
  tier, toggle, budget and always-on controls. Expected paths are explicit so a
  routing change cannot silently redefine the test's expectation.

Add a case for a new behavior or a concrete regression, not for every model name.
