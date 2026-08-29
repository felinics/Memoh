# @memohai/desktop

Memoh Desktop is the native client for Memoh Cloud or a hosted Memoh server. It
is built with [electron-vite](https://electron-vite.github.io/) and owns the
Electron shell, tray, window/menu behavior, native cache invalidation, and
renderer bootstrap. It does not start or package a local server.

The renderer owns its own `src/renderer/src/main.ts`, root component, and
router. It imports reusable building blocks from `@memohai/web` (pages,
layouts, stores, i18n, `api-client`, and `style.css`) but assembles the Vue app
locally. Desktop-only renderer setup belongs in this entry, not in
`@memohai/web`.

### How the reuse is wired

- `@memohai/web/package.json` exposes pages, layouts, stores, i18n,
  `api-client`, and `style.css` through its `exports` field.
- Vite (via `electron.vite.config.ts`) resolves those subpaths to the real
  files in `apps/web/src/` at bundle time.
- `vue-tsc` is pointed at local type stubs in `src/renderer/types/web-stubs.d.ts`
  via tsconfig `paths`, so desktop's typecheck does **not** descend into
  `apps/web/src/` or `packages/ui/src/` (those have their own CI).

## Development

```bash
# from repo root
pnpm --filter @memohai/desktop dev
# or via mise
mise run desktop:dev
```

`MEMOH_DESKTOP_BASE_URL` overrides the server that Desktop connects to. Dev
defaults to `http://localhost:18080`.

## Build

```bash
pnpm --filter @memohai/desktop build           # full platform installer
pnpm --filter @memohai/desktop build:dir       # unpacked app dir (CI smoke test)
pnpm --filter @memohai/desktop build:mac       # macOS DMG + ZIP, arm64 + x64
pnpm --filter @memohai/desktop build:linux:x64 # Linux AppImage + deb + rpm
pnpm --filter @memohai/desktop build:win:x64   # Windows NSIS installer
```

Output goes to `apps/desktop/dist/`.

All packaging commands run through `scripts/build.mjs`. It loads
`apps/desktop/.env` first, but only fills missing values, so an invoking shell
or CI environment always wins. Copy `.env.example` when building locally.
The script validates release metadata, maps signing aliases, materializes an
App Store Connect key when necessary, and invokes `electron-builder` with
`--publish never`. Builds never upload artifacts themselves.

When `MEMOH_DESKTOP_UPDATE_BASE_URL` is set, electron-builder emits
`latest.yml`, `latest-mac.yml`, or `latest-linux.yml` plus blockmaps and embeds
an `app-update.yml` pointing at that public HTTP(S) directory. The packaged app
then checks that feed on startup; download and restart/install remain explicit
actions on the About page. Without this variable, the updater is disabled and
does not fall back to an OSS or vendor feed.

The OSS Release workflow intentionally does not build or attach Desktop
installers. Clone the repository and run the platform build command above, or
use a downstream distribution workflow that signs and publishes the generated
files.

### Remote ACP adapters

On macOS and Linux, Desktop can expose this computer as a Remote ACP Runtime.
The package includes exact JavaScript adapters (`codex-acp` 1.2.0 and
`claude-agent-acp` 0.66.0), executed with Electron's bundled Node.js 24. It does
not require a system Node/npm installation and does not redistribute either
provider's native CLI. Desktop discovers an independently installed `codex` or
`claude` executable and gives it to Runtime through a Main-process-only typed
descriptor. Windows does not advertise these bundled adapters in the first
release.

Adapter and native-agent paths stay in Electron Main and are never exposed to
the renderer or sent in the Runtime handshake. Remote ACP does not copy or
manage Codex/Claude credentials or configuration; the local tools own their own
setup.

Every package build runs an unpacked-app smoke test with the packaged Electron:
it checks Node.js 22+, exact adapter versions, and absence of optional native
agent packages.

### Build environment

| Variable | Purpose | GitHub setting |
|---|---|---|
| `MEMOH_DESKTOP_BASE_URL` | Hosted server compiled into the app | Variable |
| `MEMOH_DESKTOP_VERSION` | Installer/app version override (`v1.2.3` or `1.2.3`) | Variable |
| `MEMOH_DESKTOP_APP_ID` | Reverse-DNS bundle/application id override | Variable |
| `MEMOH_DESKTOP_UPDATE_BASE_URL` | Public generic update-feed URL; also enables update metadata | Variable |
| `MEMOH_DESKTOP_REQUIRE_MAC_SIGNING` | Fail macOS release builds unless signing and notarization inputs are complete | Variable |
| `APPLE_CERTIFICATE` / `CSC_LINK` | `.p12` path/URL or base64 certificate | Secret in CI |
| `APPLE_CERTIFICATE_PASSWORD` / `CSC_KEY_PASSWORD` | `.p12` password | Secret |
| `APPLE_API_KEY` | App Store Connect `.p8` path, PEM, or base64 PEM | Secret |
| `APPLE_API_KEY_ID` | App Store Connect key id | Variable |
| `APPLE_API_ISSUER` | App Store Connect issuer id | Variable |
| `WIN_CSC_LINK` | Windows signing certificate input understood by electron-builder | Secret in CI |
| `WIN_CSC_KEY_PASSWORD` | Windows signing certificate password | Secret |

GitHub Variables are appropriate for non-sensitive identifiers, URLs, versions,
and boolean switches. Certificate/key bodies and passwords belong in GitHub
Secrets. A local filesystem path is not itself sensitive, but CI normally
stores the certificate body in the corresponding Secret.

## Icons

All app icons are checked in under `build/` and `resources/`. They are generated
from `apps/web/public/logo.svg` (the brand mark) by `scripts/build-icons.mjs`.
The generator and its dependencies are part of `@memohai/desktop`; there is no
separate icon-tools package. Re-run it after the logo changes:

```bash
pnpm --filter @memohai/desktop icons
```

Outputs:

| File | Purpose |
|---|---|
| `build/icon.icns` | macOS bundle icon (16…1024 + @2x) — packaged into `.app/Contents/Resources/` |
| `build/icon.ico` | Windows installer / `.exe` icon (16/24/32/48/64/128/256) |
| `build/icon.png` | Linux `.deb`/`.rpm`/`.AppImage` icon (1024×1024) |
| `resources/icon.png` | Runtime `BrowserWindow.icon` + macOS dev `app.dock.setIcon` (512×512); bundled via `asarUnpack` |
| `resources/tray-icon.png` | Runtime tray/menu bar icon |

`build/icon.icns` requires macOS (`iconutil`); the script skips it on other
platforms.
