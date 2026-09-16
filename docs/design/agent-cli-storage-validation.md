# Agent CLI storage implementation validation

> Historical validation record: the configurable store used below has since been removed.
> Current paths are fixed: OSS `/data/.memoh/deps`, Cloud `/opt/memoh/deps`.

Validation dates: 2026-09-14–15. Status: implementation and validation in progress.

This record accompanies the [implementation plan](agent-cli-storage-and-single-install-plan.md). Results below describe completed checks, not release approval. Human QA has not been performed.

## Fixed-path update on 2026-09-15

The dependency-store setting and provider override were removed. OSS uses `/data/.memoh/deps`; Cloud uses `/opt/memoh/deps`. Agent Homes remain under `/data`.

On the rebuilt OSS development stack at `http://localhost:18082`, Computer Use opened the QA Bot's Codex App, selected reinstall, reviewed the exact `0.154.0` target, and completed installation using the local companion registry. The UI reported completion at `/data/.memoh/deps/codex/installs/1bf0c34d86475343facd34428625539a/bin/codex`. A separate live workspace check resolved the current payload to that directory and returned `codex-cli 0.154.0` from the managed launcher. See [the actual installation screenshot](../evidence/agent-cli-storage/fixed-path-codex-installed.png).

Authenticated OSS Codex execution passed through Computer Use after the user selected the test account. A fresh official device-login flow connected the Codex Agent. In the chat UI, Codex used the displayed default model (`GPT-6-Astra`, medium reasoning), executed two shell commands, and reported the path and contents of the file it created. A separate read from the actual workspace confirmed `/data/codex-runtime-proof-20260915.txt` contained exactly `MEMOH_CODEX_OK_20260915` (23 bytes, no trailing newline; SHA-256 `0005bf2549f44ce55eaaf4b8c6c705d6800294db53184cbd83fef24da5cea41a`). The running native Codex process resolved to the new fixed-path installation, rather than a different image executable. See [the actual authenticated conversation and command records](../evidence/agent-cli-storage/codex-authenticated-file-proof.png).

The expanded command panel displayed “暂无输出” even though Codex's final response and the independently read file contained the correct text; raw command-output rendering is not validated by this result. The composer also labeled the default folder unavailable while the actual run used `/data`. Neither UI issue was changed in this narrowly scoped update. This check covers one authenticated turn with file creation and reading, not reconnect/resume, token refresh, or Cloud/NFS behavior. Cloud runtime/UI testing was not repeated, as requested.

## Historical local environment

- OSS implementation baseline: `58741b308`; branch `codex/agent-cli-local-store`.
- Development stack: `devenv/docker-compose.yml`, with an ignored local configuration selecting `container.dependency_store_root = "/var/lib/memoh/deps"` and the local Supermarket at `http://host.docker.internal:5175`.
- Server: `http://localhost:18080`; Web: `http://localhost:18082`; local Supermarket: `http://localhost:5175`.
- Workspace: Linux ARM64, glibc, containerd, image `docker.io/memohai/workspace:debian`, image digest `sha256:36bb5415bd4228aac81b114dd3a33d00ac3d4dd9b1d1077c85adbc83ad7b04d5`.
- Disposable Bot: `agent-cli-storage-qa-20260914` (`2fe80839-9793-455a-bcea-4f5b740fb4a9`). Existing Bots and their volumes were not rebuilt.

## Installation and rootfs reconstruction

The actual Web flow prepared and confirmed Codex `0.154.0` and Claude Code `2.1.270`, including their frozen recipe revisions and automatic-repair consent. Both App progress dialogs reached completion; the Claude run included blank npm output without a false disconnect. The backend committed a new payload under `/var/lib/memoh/deps/codex/installs/<operation-id>` and durable metadata under `/data/.memoh/deps`.

The Node App linked the existing image Node without granting new recovery authority. An explicit management operation then installed Node `24.4.1`, distinct from the image's `24.14.0`, and pnpm `12.4.1`.

The reconstruction check called the real container DELETE API with `preserve_data=true`, verified the container no longer existed in containerd, and recreated it through the real API with data restoration. A marker outside `/data` disappeared. No install or retry request was sent after recreation; the workspace-ready worker restored the authorized dependencies.

| Dependency | Before deletion | After automatic repair | Result |
| --- | --- | --- | --- |
| Codex | `0.154.0`, local payload | `0.154.0`, new installation ID | CLI version command succeeded |
| Node | `24.4.1`, overriding image `24.14.0` | `24.4.1`, new installation ID | No substitution with image version |
| pnpm | `12.4.1`, local payload | `12.4.1`, new installation ID | CLI version command succeeded |
| Claude Code | `2.1.270`, local payload | `2.1.270`, new installation ID | CLI version command succeeded |

The final four-dependency run used the rebuilt local-control image. All four desired versions, recipe revisions, authorization revisions, and original authorization times remained unchanged. A Server hot reload interrupted a GET observation; reconnecting observation confirmed all four automatic operations completed, without another install or retry request. The final managed Codex launcher uses `/run/memoh/deps/.leases`. A test-only ordinary `auth.json` under `/data/.codex/agents/storage-qa` retained its SHA-256 hash and file mode. This demonstrates file persistence; it does not establish successful OAuth refresh or authenticated model calls.

The earlier three-dependency run observed all dependencies ready 48.8 seconds after container recreation returned. This is one local sample, not a performance distribution or an E2B/NFS benchmark. It excludes authenticated initialization and a model turn.

The first run exposed a legacy pnpm failure: archive restoration preserved metadata and directories but omitted package symlinks. The old frozen recipe was not silently replaced. After an explicit management operation selected a newly published isolated recipe, the second full reconstruction succeeded. The shared generated recipes now support isolated publication and custom command/import health probes; bundle resolution metadata remains persistent and must match its requested package-map digest.

## Defects found during real validation

- A real Cloud E2B workspace mounted `/data` over NFSv3. A dependency kernel lock on that volume hung in `rpc_wait`, while a lock on local storage completed immediately. Linux transaction, lease, and startup-window locks now use the fixed local `/run/memoh/deps` control root, independent of the configured payload root. Durable receipts and state remain under `/data`. E2B replaces `/run` during boot, so its template startup must create the control directory before declaring readiness. The final OSS four-dependency reconstruction passed with this control layout; integrated Cloud repair is tracked below. OSS bind-mounts its host runtime directory at `/run/memoh`, so the actual control filesystem must be local and writable, and these files can outlive a rootfs. Safety depends on kernel locks and the complete workspace epoch, not on deleting the control files.
- The first live same-version replacement kept an initialized Codex app-server responsive and retained its old payload, but stop/start did not invoke maintenance. After correcting the ordinary restart path, the final live check passed: the old app-server answered `config/read` after reinstall, the old payload remained both during execution and after process exit, and a complete stop/start collected only the retired payload while the current launcher remained usable.
- Blank npm output was serialized without an SSE `data` string, causing the App client to report a connection interruption despite a successful backend install. Empty log lines now retain the field.
- Readiness used a different version parser from installation and rejected Node's `v` prefix. Both boundaries now use the frozen recipe's probe, or the standard executable version probe when no custom probe exists.
- Recipes without custom probes previously lacked a Server-side candidate execution check. Candidate validation now checks the executable before committing.
- Recovery previously trusted mutable workspace receipt fields for the requested version, action and authorization identity. A database-owned operation intent now binds these fields when the operation is claimed; an unmatched or legacy receipt cannot create authorization.
- Frozen repair graphs now use stored approved definitions; an empty or changed current catalog cannot substitute another prerequisite graph. Preparation failures compare the originally observed status so an old observer cannot overwrite a peer's running operation.
- The Apps detail query could retain an old repair failure after the worker recovered the dependency. Visible-page polling and activation/navigation refresh now read actual backend state without authorizing installation.

Screenshots and a sanitized machine-readable outcome are available in [the evidence directory](../evidence/agent-cli-storage/README.md).

## HTTP authorization and query contracts

A separate local test principal received only `workspace_read` on the disposable Bot. The final 47-scenario real HTTP run passed after the lifecycle and control changes: unauthenticated requests returned 401; management operations returned 403 for the read-only principal; List, refresh and Preflight remained readable; malformed or incomplete confirmation targets returned 400; an unavailable immutable publication returned 503. Valid exact preparation and repair preparation returned 200 without starting operations. The removed Rollback endpoint returned 404 and script preview rejected the removed action.

Installation operation fields, the desired targets and authorization-event counts were compared before and after each request and remained unchanged. The temporary grant was removed and its membership deactivated. Launcher resolution has no public HTTP endpoint, so its non-installing behavior is tested at the Go boundary rather than claimed as HTTP coverage.

## Native state decision

The [Codex native-state evaluation](agent-cli-native-state-evaluation.md) contains the completed directory and state-loss experiments. Moving all native SQLite state to ephemeral storage lost goal state after rootfs replacement. Therefore this implementation retains the persistent Codex Home and ordinary `auth.json`; it does not enable ephemeral `sqlite_home` globally.

## Default OSS store verification

OSS default-store verification passed with one entrypoint repair. A disposable Bot in an isolated Server, PostgreSQL database and containerd namespace used `dependency_store_root = ""`. The real UI reviewed and confirmed uv 0.12.12 and frozen definition `c08c6b3cd8ad226a7652c1f67ac42eb24dc65aeab75f2245c99cdcc8b65f4667`. Its managed payload was installed under `/data/.memoh/deps/uv/installs/`, and the real managed launcher returned `uv 0.12.12 (aarch64-unknown-linux-gnu)`.

A real preserve-data delete/recreate replaced the root filesystem. The payload installation ID, path, executable hash, exact version, desired revision and original authorization audit remained unchanged, and the managed launcher still executed successfully. This was not a zero-operation rebuild: the existing containerd archive omits symlinks, so the ready handler restored `current` through one trusted entrypoint repair. It reused the existing payload and did not run the download recipe. Data restoration completes before workspace startup; no readiness-before-restore race was observed.

The fresh fixture account used the normal authenticated `PUT /users/me` onboarding preference contract after the Bot was created by API. Its user ID matched the Bot owner; no model or Agent credentials were added. Existing 18080 settings, Bots and credentials were unchanged. The screenshots show confirmation, completed installation and the fresh post-rebuild UI. This is agent verification, not human QA or E2B performance evidence.

See the [default-store evidence](../evidence/agent-cli-storage/default-store-runtime-evidence.md) and its structured runtime results.

After verification, the isolated Server on 18100, Web on 18102, proxy and Bot task were stopped. The fixture database, namespace metadata, snapshot and data were retained for review. The original development Server on 18080 remained healthy. The default-store screenshots therefore document the completed run; port 18102 is no longer serving it.

## Cloud and E2B

The final dedicated E2B template (`6iq72gy5qp8lziulxgb1`, alias `memoh-agent-cli-control-v2-test-20260914`) creates local kernel-control storage during boot. The real-provider volume/rebuild/lock test passed in 21.58 seconds as UID 1000 and confirmed deletion of its temporary sandboxes and volume. This mechanism test uses synthetic payloads; integrated CLI results are recorded separately below. Production defaults were not changed.

The isolated Cloud development stack at `http://localhost:27100` used normal BFF login, dedicated databases, a QA Team/Bot and the local Supermarket. Actual Codex `0.154.0` installation placed its payload on local storage and persisted the approved target; recipe execution to persisted desired state took about 19.2 seconds in this sample, including six seconds reported by npm. Pause/resume retained the sandbox and synthetic Home fixture hashes. A real 1 MiB filesystem capacity failure preserved the existing CLI, approved version/recipe and Home. The failed reinstall advanced the concurrency generation as intended.

Deleting the real sandbox while retaining its volume removed the payload. A QA-only closed npm proxy produced a download failure and a 60-second repair backoff. After removing that test configuration, Retry recovery restored the exact frozen Codex target into a new installation with unchanged desired revision and Home hashes. The fresh Apps page showed Installed.

The Cloud implementation was merged with its current `submodule/memoh` baseline, then the Server was rebuilt and restarted. A new real sidebar flow reviewed and installed Claude Code `2.1.270`, retained the originating Bot, and returned to the same chat with the Installed list refreshed. Both managed CLIs returned their exact versions from the final running source. The final merged checks passed 139 Web tests, SDK type checking, the Web build, scoped ESLint, real PostgreSQL dependency/Team-RLS tests, the complete handlers suite, and five real Linux adapter/lifecycle tests without skips. Cloud screenshots and sanitized logs remain in the private Cloud repository, linked from PRs #352 and #354.

Native app-server startup with the persistent NFS Home remains blocked: Codex 0.154.0 first hangs on a lock under `CODEX_HOME/tmp/arg0`. The same installed binary initializes with a disposable local Home. Moving only a disposable test Home's `tmp/arg0` onto local storage passes that lock, then blocks in a `state_5.sqlite` `fcntl` lock; after 34.75 seconds SQLite runtime initialization fails and initialize remains incomplete. The dependency control-root change does not fix this native runtime gate. No production Home, SQLite or NFS mount change was made. See the native-state evaluation for exact-version source evidence. Successful version commands and synthetic Home hashes do not establish authenticated turns or resume.

## Remaining acceptance work

- Resolve the demonstrated native Codex Home lock/SQLite failure on E2B NFS without losing persistent state.
- Validate authenticated Codex and Claude turns with authorized test credentials, including persistence and resume.
- Finish checking the final draft PR heads, keeping Human QA unchecked. All four related draft PRs have been submitted; current screenshots and sanitized results are versioned with the implementation.

## Repository checks

The seven benchmark protocol tests pass. The full `mise run lint` currently stops at the pre-existing `animate-spin` UI-contract violation in `apps/web/src/pages/home/components/tool-call-diff-panel.vue:6`; that file matches the implementation baseline and is unchanged by this task. Full ESLint initially scanned an unrelated nested `.claude/worktrees` checkout and crashed because that checkout uses an older parser. The main repository passed `pnpm exec eslint . --ignore-pattern '.claude/**'`: zero errors and three unchanged test warnings. The 34 changed Web files passed scoped lint without warnings. No dependency reinstall or unrelated source change was needed. The changed Web test suite passed 99 tests across 11 files after regenerating the SDK; SDK type checking passed. Full Web type checking remains blocked by pre-existing UI package alias errors. The final affected Go runtime and workspace suites passed. The final old-intent migration additions also passed scoped Linux lint. Linux race tests covered dependency execution and bridge admission; full PostgreSQL integration passed with 256 passing test events, zero failures and zero skips, and the desired-state race run also passed. `GOOS=linux CGO_ENABLED=0 mise run lint:go` passed in 216.82 seconds for the complete Linux source tree.

The first commit hook run hit the unchanged `TestIdleTimeoutToolCallRearmsCurrentWindow` timing test (a 50 ms sleep against an 80 ms deadline) under parallel checks. The isolated test then passed 20 consecutive runs; the complete commit hook then passed without changing that unrelated test.

## Non-root Linux CI correction (2026-09-15)

The first remote Go Test run exposed a cross-UID `/proc/1/ns/pid` permission failure and a dependent legacy-epoch test panic. Epoch collection now reads the caller's own namespace only after a single `NSpid` proves that `/proc` represents the same PID namespace; boot ID and PID 1 start time remain required. Incomplete or ancestor-namespace evidence still fails closed. Normal epoch values remain unchanged.

Real Linux tests with a root PID 1 and UID 1000, plus separate root runs, passed all bridge, payload-lease and dependency race suites without skips. Non-root collection correctly retains payloads when other processes cannot be inspected. Scoped Linux lint passed. The development bridge was rebuilt, and live Codex same-version replacement followed by the real stop/start API again passed: the old app-server responded, the old payload survived until full restart, and the replacement CLI remained usable. Fresh screenshot and sanitized runtime evidence are in `docs/evidence/agent-cli-storage`.

## Related contributions

OSS draft PR [#1258](https://github.com/felinics/Memoh/pull/1258) contains the implementation and public screenshot evidence. At code commit `46eee328605d420510ea9ebdaf00a2a77a4ef842`, Go tests, lint, migrations, runtime checks and platform builds passed, as did PR Format. The publish job was intentionally skipped. Subsequent evidence-only commit checks are tracked on the PR.

Cloud outer draft PR [#352](https://github.com/felinics/Memoh-Cloud/pull/352) contains template/configuration changes and the real Cloud installation, capacity failure, rebuild/backoff/retry and NFS Home failure evidence. It does not switch production templates.

Cloud inner draft PR [#354](https://github.com/felinics/Memoh-Cloud/pull/354), targeting `submodule/memoh`, contains the provider-neutral dependency implementation, Team/RLS migration, final merged checks and fresh sidebar installation evidence. The outer PR does not point its gitlink at the unmerged feature branch. CI for the inner PR is tracked separately from the passing local checks.

Supermarket draft PR [#26](https://github.com/felinics/supermarket/pull/26) has passed both CI checks and contains the 32 recipe updates, generated lock and versioned screenshots from the real Memoh installation flow. It does not deploy the registry.
