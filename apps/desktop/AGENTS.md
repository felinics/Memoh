# Desktop App (apps/desktop)

## Overview

`@memohai/desktop` is the Memoh Electron Desktop client for Memoh Cloud or a
hosted Memoh server. It reuses Vue pages, stores, i18n, API setup, and design
tokens from `@memohai/web`, but owns the native Electron shell: windows, tray,
menus, keyboard integration, cache invalidation, preload IPC, and the renderer
bootstrap.

Desktop does not start a local server, package database files, embed Qdrant, or
install a companion CLI. It embeds the `@memohai/runtime` SDK plus exact
JavaScript-only ACP adapters so the Electron main process can connect this
computer to the hosted server as a trusted Remote Runtime. Native Codex and
Claude Code executables are user-installed and are never redistributed in the
Desktop package. Use `MEMOH_DESKTOP_BASE_URL` to point the app at the target
server. The default dev target is `http://localhost:18080`.

## Tech Stack

| Category | Technology |
|----------|-----------|
| Shell | Electron 34 |
| Bundler | electron-vite 4 |
| Renderer | Vue 3 + Vite 8 + Tailwind CSS 4 |
| Packager | electron-builder 26 |
| Reused packages | `@memohai/web`, `@felinic/ui`, `@memohai/sdk`, `@memohai/icon`, `@memohai/config`, `@memohai/runtime` |
| Type checking | TypeScript + `vue-tsc` |

## Directory Structure

```
apps/desktop/
├── electron.vite.config.ts        # main / preload / renderer Vite config
├── electron-builder.yml           # single Desktop package config
├── package.json
├── scripts/
│   ├── build.mjs                  # env + signing + electron-vite + electron-builder
│   └── build-icons.mjs            # checked-in app/tray icon generator
├── src/
│   ├── main/index.ts              # Electron main process and IPC handlers
│   ├── main/remote-runtime.ts     # safeStorage-backed Remote Runtime lifecycle
│   ├── preload/index.ts           # typed renderer bridge
│   ├── preload/global.d.ts        # renderer API typings
│   ├── shared/remote-runtime.ts   # narrow structured-clone state/config types
│   └── renderer/
│       ├── src/main.ts            # Vue renderer bootstrap
│       ├── src/chat/App.vue       # persistent shell root
│       └── types/                 # local stubs for reused web/ui exports
├── resources/
│   ├── icon.png
│   └── tray-icon.png
└── build/                         # packager input icons
```

## Reuse from @memohai/web

The renderer imports web modules through public subpath exports in
`apps/web/package.json`, including:

- `@memohai/web/style.css`
- `@memohai/web/i18n`
- `@memohai/web/api-client`
- `@memohai/web/store/settings`
- `@memohai/web/lib/desktop-shell`
- `@memohai/web/layout/main-layout/index.vue`
- `@memohai/web/components/sidebar/index.vue`
- `@memohai/web/components/settings-sidebar/index.vue`
- `@memohai/web/pages/**/*.vue`

Do not import the full web `main.ts`. Desktop has its own bootstrap so it can use
memory-history routing, provide `DesktopShellKey`, wire native menus into the
shared command registry, and keep native cache synchronization out of the web
bundle.

## Type Stubbing

`vue-tsc` follows symlinks. Desktop's renderer typecheck is intentionally scoped
with `tsconfig.web.json` path stubs:

- `src/renderer/types/web-stubs.d.ts` for `@memohai/web/*`
- `src/renderer/types/ui-stubs.d.ts` for `@felinic/ui`

When adding a new reused web or UI import, update the matching stub. Keep the
runtime import specifier and the typecheck stub aligned; do not switch to private
source aliases just to silence type errors.

## Main Process

`src/main/index.ts` owns:

- app identity and single-instance behavior
- BrowserWindow creation and macOS chrome
- tray creation and dock/menu focus behavior
- native menu accelerators
- external URL handling
- cache invalidation broadcast
- renderer `/api` proxy target via `MEMOH_DESKTOP_BASE_URL`
- encrypted Remote Runtime configuration and `RuntimeSession` lifecycle
- fixed ACP adapter discovery and local Codex/Claude CLI selection

The preload bridge is the only renderer API surface for Electron/main-process
behavior. Keep it small and typed in both `src/preload/index.ts` and
`src/preload/global.d.ts`.

Current Desktop IPC includes:

- `desktop:server-status`
- `desktop:api-base-url`
- `desktop:runtime-state`
- `desktop:configure-runtime`
- `desktop:updates:get-info`
- `desktop:updates:get-state`
- `desktop:updates:check`
- `desktop:updates:download`
- `desktop:updates:install`
- `desktop:updates:state-changed`
- `desktop:set-menu-accelerators`
- `desktop:open-external-url`
- `desktop:broadcast-invalidate`
- `window:close-self`

Remote Runtime IPC is intentionally narrower than the SDK: the renderer may
only read status or pass `{ runtimeId, name, key } | null`. `name` is the
user-chosen Runtime display name; server URL, workspace base, OS device name,
localhost policy, filesystem paths, and commands are owned by Main.
Do not add IPC for local database auth, project-folder picking, server lifecycle,
arbitrary filesystem/command access, or CLI installation.

`src/main/acp-adapters.ts` is the only Desktop adapter resolver. It passes
fixed Main-owned absolute paths to `RuntimeSession`, never to IPC. Both aliases
must be present: a descriptor when a local CLI is found, or `false`
to suppress ambient PATH fallback. macOS and Linux are supported; Windows must
keep both aliases disabled. Desktop and Runtime launch the local process but do
not own provider authentication or local Agent configuration.

## Renderer

`src/renderer/src/main.ts` creates the Vue app, installs the reused web plugins,
sets the SDK base URL from the main-process status, and registers desktop cache
sync. Authentication belongs to the hosted server flow; the renderer should not
inject local auto-login tokens.

`chat/App.vue` provides `DesktopShellKey` and the narrow `DesktopRuntimeKey`
and `DesktopUpdatesKey` bridges so reused web components can adapt to Electron
without importing Electron or receiving Node privileges.

## Commands

```bash
pnpm --filter @memohai/desktop dev
pnpm --filter @memohai/desktop typecheck
pnpm --filter @memohai/desktop build:dir
pnpm --filter @memohai/desktop build
```

`build:dir` is the CI smoke path for an unpacked app. Packaged output goes to
`apps/desktop/dist/`.

## Packaging Rules

`electron-builder.yml` is the only Desktop packaging config. The product name is
`Memoh`, and packaged resources should only include the Electron application,
icons, compiled app bundles, and the `@memohai/runtime` JavaScript SDK/proto used
by Main. Build the Runtime package before Desktop and keep `bridge.proto`
unpacked for the gRPC loader. Do not add server binaries, installed CLI
binaries, database files, provider templates, container runtimes, Qdrant, or
media runtimes to Desktop packaging.

Remote ACP packaging pins `@agentclientprotocol/codex-acp` and
`@agentclientprotocol/claude-agent-acp` exactly and leaves their JavaScript
dependency graph inside `app.asar`. The two optional platform-native agent
package families must remain excluded: users supply their own Codex and Claude
Code CLIs. Keep the `runAsNode` fuse enabled, the packaged Electron/ASAR smoke
hook green, adapter LICENSE files present, and `resources/THIRD_PARTY_NOTICES.md`
current.

`scripts/build.mjs` owns build-time environment loading and signing setup.
Process/CI values override `apps/desktop/.env`; it always passes
`--publish never`, so release workflows upload completed artifacts rather than
delegating network publication to electron-builder. A configured
`MEMOH_DESKTOP_UPDATE_BASE_URL` enables generic update metadata and the native
updater. Without it, updater checks stay disabled.

## Icons

Checked-in icons live in `build/` and `resources/`. Regenerate them only when the
brand mark changes:

```bash
pnpm --filter @memohai/desktop icons
```
