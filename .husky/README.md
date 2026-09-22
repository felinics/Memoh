# Commit checks

`pre-commit` checks staged blob sizes, then runs relevant checks in sequence.
It does not commit or push. `lint-staged` keeps its backup and partial-staging
behavior. Go tools still read the working tree, as they did before this hook;
keep unstaged Go edits in mind when interpreting results.

- JavaScript, TypeScript and Vue changes: ESLint through `lint-staged`.
  ESLint does not validate Markdown, JSON or SCSS in this repository.
- Frontend dependency, UI submodule, ESLint or TypeScript configuration changes: full ESLint.
- Go sources and resources below `cmd`, `internal`, `db` or `templates`:
  lint and test the nearest indexed Go package. This includes embedded assets
  and test fixtures. Deleting the last Go source in a package falls back to
  full Go checks; renames consider both paths.
- Go dependencies, lint/toolchain configuration, `conf/` and SQL inputs: full Go checks.
- Other files: no language checks unless full mode is requested.

Package-scoped checks do not test all reverse dependencies. Go CI retains
whole-module lint and race tests, with resource/fixture paths included in its
triggers. Cloud must also enable these workflows for `submodule/memoh`.

Run `MEMOH_FULL_CHECKS=1 sh .husky/pre-commit` for full Go and ESLint checks.
Run `node .husky/checks.mjs --plan` to inspect selection without running checks.
The `check-go`, `check-go-test`, `check-web` and `check-large-files` wrappers
remain available and use the same staged selection.

Each stage reports its command and duration. Local stages have a 180-second
wall-clock budget (900 seconds in explicit full mode). Override it with
`MEMOH_CHECK_TIMEOUT_SECONDS=300`; timeout or missing tools fail the check,
never silently pass. Go tests have the same per-test-binary timeout as well.
Cancellation terminates the running process group on Unix. Do not run multiple
Go lint jobs against the same shared linter cache concurrently.

Verify the hook with `node --test scripts/hook-checks.test.mjs`. Tests use
isolated Git indexes and fake tools; real-tool benchmarks remain separate.
