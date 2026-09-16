# Agent CLI storage UI evidence

Captured from the running OSS development application at `http://localhost:18082` on 2026-09-14 using the disposable `Agent CLI Storage QA` Bot and the local Supermarket registry. These are actual application screenshots. Backend and rootfs evidence is recorded in [the validation report](../../design/agent-cli-storage-validation.md).

- [Codex confirmation](codex-install-confirmation.jpg): exact version, immutable recipe revision, and automatic recovery consent before installing the App.
- [Claude Code confirmation](claude-install-confirmation.jpg) and [completion](claude-install-completed.jpg): an actual install completed through the App progress dialog, including blank npm output.
- [pnpm confirmation](pnpm-final-recipe-confirmation.jpg) and [completion](pnpm-final-reinstall-completed.jpg): explicit adoption of the final isolated recipe followed by a successful same-version reinstall.

- [Automatic recovery](node-pnpm-final-repairing.jpg) and [recovered Node/pnpm](node-pnpm-final-restored.jpg): the open application automatically refreshed from image Node `24.14.0` toward the approved `24.4.1`, then showed both dependencies ready without another install/retry request.
- [Codex actions](codex-final-no-rollback.jpg) and [restored Claude Code](claude-final-restored.jpg): final installed versions after reconstruction; the dependency menu has no Rollback action.

The screenshots establish the management flow, not authenticated model calls or human QA. They contain no Agent credentials. JPEG extensions match the bytes emitted by the browser capture API.

Sanitized [runtime outcomes](runtime-results.json) and [47 HTTP contract outcomes](http-results.json) record the backend checks alongside the screenshots.

After the Linux non-root epoch fix, the bridge was rebuilt and the live same-version replacement/stop/start flow was repeated on 2026-09-15. [Fresh UI](codex-epoch-fix-verified.jpg) and [sanitized runtime results](epoch-fix-runtime-results.json) show the current installed version, old-process continuity and cleanup only after full restart. Separate real root/UID 1000 Linux race suites passed without skips.

The isolated default-store run at `http://localhost:18102` verified uv under `/data`: [confirmation](default-store-uv-confirmation.jpg), [installed path](default-store-uv-installed.jpg), and [after rootfs reconstruction](default-store-uv-after-rebuild.jpg). [Details](default-store-runtime-evidence.md) and [runtime outcomes](default-store-runtime-results.json) distinguish unchanged payloads from the single entrypoint repair required because the existing archive omits symlinks.

The isolated 18100/18102 processes and Bot task were stopped after verification. Their fixture data was retained; the original 18080/18082 development environment was preserved.
