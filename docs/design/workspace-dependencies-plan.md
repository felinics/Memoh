# Supermarket dependency implementation stack

The specification is [workspace-dependencies.md](workspace-dependencies.md).
Implementation spans a Supermarket PR and the existing six Memoh stacked PRs.
The original layers are rewritten, not extended with another Memoh-only layer.

## Cross-repository prerequisite

Supermarket introduces the official Dependency resource, deterministic artifacts,
immutable releases, separate dependency snapshot/lock, local/R2 publication,
HTTP catalog/release/artifact endpoints, localized metadata and the five recipes.
The shared registry commands accept `--kind all|skills|dependencies` (default all).
Its publisher tests and ShellCheck own recipe validation. A fixture export command
feeds the Memoh consumer contract tests.

The Supermarket protocol must be available before Memoh is deployed against it.
Local integration uses the paired Supermarket checkout and fresh Memoh data volumes.
No production publication or data deletion is implied by the code PRs.

## Memoh layers

| PR | Branch | Responsibility |
| --- | --- | --- |
| #1132 | `workspace-deps/00-docs` | Cross-repository design, latest policy, cache/offline behavior and this plan |
| #1135 | `workspace-deps/01-core` | Bridge half-close/reset, portable definition validation, artifact loader, runner, discovery and immutable snapshots; no embedded recipes |
| #1137 | `workspace-deps/02-service-api` | PostgreSQL cache/provenance, remote catalog adapter, service state machine, operation preparation, HTTP/SSE and generated SDK |
| #1138 | `workspace-deps/03-runtime` | Official runtime bindings, cached launcher lookup, nonblocking first-use install, handshake observations and PATH precedence |
| #1141 | `workspace-deps/04-web` | Remote metadata/icons, cache retry notices, prepared revision transport, enable flow, operation store and three locales |
| #1144 | `workspace-deps/06-image-contract` | Remove image-baked Agent CLIs and workspace-contract gate; validate the complete first-use installation flow |

## Delivery discipline

1. Pin and inspect both repository baselines before implementation. Preserve each
   remote branch's old SHA and retain backups before rewriting the stack.
2. Implement and test the producer protocol; generate immutable consumer fixtures.
3. Rewrite each Memoh layer so it builds independently on its predecessor. Keep
   generated SQL/OpenAPI/SDK files with their owning schema/API change.
4. Run paired local services and real authenticated APIs. Test a new published tool,
   definition refresh, frozen previews, offline cached behavior and actual workspace
   operations. Keep logs and receipts for review without recording access tokens.
5. Regenerate code, run the repositories' checks, inspect each layer's diff, and push
   all six refs using explicit force-with-lease protection. Preserve stack bases.
6. Open a draft Supermarket PR, update the six Memoh descriptions around their final
   implementation, and verify current-head CI summaries (including cancelled-run
   ordering issues). Keep the exact No human QA disclosure until a human verifies.

## Scope

Official `memoh` registry only. No third-party trust configuration, no embedded
fallback, no recursive dependency solver, no migration of experimental deps data,
and no change to Skill Package postinstall or to the set of Agent drivers.
