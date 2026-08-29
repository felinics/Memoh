# Web Frontend (apps/web)

## Overview

`@memohai/web` is the browser management UI for Memoh, built with Vue 3 + Vite. It provides the chat interface for interacting with bots, plus visual configuration for bots, models, channels, memory, workspace display, and more.

In deploy/server mode this package is served as the standalone Web frontend. The native desktop client reuses many of the same pages, stores, routes, i18n files, API client helpers, and design tokens through `@memohai/web` exports, but desktop owns Electron windows, local server startup, tray behavior, and bundled resources. Keep Web usable as a pure browser app.

## UI Guidance (required)

Before writing or changing anything under `apps/web` — pages, components,
layout, copy, or styling — read `packages/ui/AGENTS.md`. The UI submodule owns
the design contract and routes agents to its adjacent page-level and
layout-owner guidance. Follow that routing instead of looking for host-side
copies under `.agents/skills/`.

Do not skip this for "small" UI edits. The routed guidance covers
`@felinic/ui` composition, copy discipline, empty/loading states, layout
patterns, and the verification checklist for every surface in this package.

## Tech Stack

| Category | Technology |
|----------|-----------|
| Framework | Vue 3 (Composition API, `<script setup>`) |
| Build | Vite 8 + `@vitejs/plugin-vue` |
| CSS | Tailwind CSS 4 (CSS-based config, no `tailwind.config.*`) |
| UI Library | `@felinic/ui` (built on Reka UI + class-variance-authority) |
| State | Pinia 3 + `pinia-plugin-persistedstate` |
| Data Fetching | Pinia Colada (`@pinia/colada`) + `@memohai/sdk` |
| Forms | vee-validate + `@vee-validate/zod` + Zod |
| i18n | vue-i18n (en / zh) |
| Icons | lucide-vue-next (primary) + `@memohai/icon` (brand/provider icons) |
| Toast | `@felinic/ui` `toast` / `Toaster` (in-house) |
| Tables | @tanstack/vue-table |
| Markdown | markstream-vue + Shiki + Mermaid + KaTeX |
| Charts | ECharts + vue-echarts |
| Terminal | @xterm/xterm + @xterm/addon-fit + @xterm/addon-serialize |
| Code Editor | Monaco Editor + stream-monaco |
| Utilities | @vueuse/core, @vueuse/integrations |
| Animation | animate.css + tw-animate-css |
| TypeScript | ~5.9 (strict mode) |

## Directory Structure

```
src/
├── App.vue                    # Root component (RouterView + Toaster + settings init)
├── main.ts                    # App entry (plugins, global components, API client setup)
├── router.ts                  # Route definitions, auth guard, chunk error recovery
├── style.css                  # Tailwind imports (delegates to @felinic/ui/style.css)
├── i18n.ts                    # vue-i18n configuration
├── assets/                    # Static assets (logo.svg)
├── components/                # Shared components
│   ├── sidebar/               #   Left shell: activity bar + collapsible side panel
│   │   ├── index.vue          #     Activity rail (bot avatar, Sessions/Files/Search icons, Settings) + resizable panel host
│   │   ├── bot-switcher.vue   #     Bot switcher dropdown (rail avatar variant + row variant)
│   │   ├── panel-sessions.vue #     Sessions panel: New Session button + Recents
│   │   ├── panel-files.vue    #     Files panel wrapper (permissions + openFilesAt navigation)
│   │   ├── panel-search.vue   #     Search panel (sessions search, autofocused input)
│   │   ├── files-pane.vue     #     Workspace file browser (upload, mkdir, batch ops)
│   │   ├── recents.vue        #     Session list (type filter, rename/delete dialogs, streaming indicator)
│   │   └── session-item.vue   #     Session list row (avatar, title, timestamp, streaming spinner)
│   ├── settings-sidebar/      #   Settings section sidebar (back-to-chat + nav items)
│   ├── main-container/        #   Main content area (KeepAlive RouterView)
│   ├── master-detail-sidebar-layout/  # Master-detail layout pattern
│   ├── chat-list/             #   Chat list helpers
│   │   └── channel-badge/     #     Channel badge component
│   ├── chat/                  #   Chat UI sub-components
│   │   ├── chat-status/       #     Chat connection status indicator
│   │   └── chat-step/         #     Chat processing step indicator
│   ├── file-manager/          #   File browser (list + viewer + utils)
│   ├── terminal/              #   Terminal emulator wrapper (xterm)
│   ├── monaco-editor/         #   Monaco code editor wrapper
│   ├── model-capabilities/    #   Model capabilities display
│   ├── context-window-badge/  #   Context window size badge
│   ├── bot-select/            #   Bot selection dropdown
│   ├── form-dialog-shell/     #   Dialog wrapper for forms
│   ├── confirm-popover/       #   Confirmation popover
│   ├── loading-button/        #   Button with loading state
│   ├── status-dot/            #   Status indicator dot
│   ├── channel-icon/          #   Channel platform icon
│   ├── provider-icon/         #   LLM provider icon (icons.ts + index.vue)
│   ├── search-provider-logo/  #   Search provider icons (custom-icons.ts + index.vue)
│   ├── searchable-select-popover/  # Searchable dropdown
│   ├── timezone-select/       #   Timezone selector
│   ├── key-value-editor/      #   Key-value pair editor
│   ├── import-models-dialog/  #   Bulk model import dialog
│   ├── add-platform/          #   Add platform dialog
│   ├── add-provider/          #   Add LLM provider dialog
│   └── create-model/          #   Create model dialog
├── composables/               # Reusable composition functions
│   ├── api/                   #   API-related composables
│   │   ├── useChat.ts         #     Aggregated re-export of chat composables
│   │   ├── useChat.types.ts   #     Bot, Session, Message, StreamEvent types
│   │   ├── useChat.chat-api.ts  #   Bot/session CRUD (fetchBots, fetchSessions, etc.)
│   │   ├── useChat.message-api.ts  # Message REST + SSE wrappers (per-session messages, bot-wide activity)
│   │   ├── useChat.ws.ts      #     WebSocket connection (send, abort, reconnect)
│   │   ├── useChat.ws.test.ts #     WebSocket tests
│   │   ├── useChat.content.ts #     Message content parsing (tool calls, text, reasoning)
│   │   ├── useContainerStream.ts  # Container creation SSE stream
│   │   └── usePlatform.ts     #     Platform list query + create mutation
│   ├── useDialogMutation.ts   #   Mutation wrapper with toast error handling
│   ├── useRetryingStream.ts   #   SSE retry with exponential backoff
│   ├── useSyncedQueryParam.ts #   URL query param sync
│   ├── useBotStatusMeta.ts    #   Bot status metadata
│   ├── useAvatarInitials.ts   #   Avatar initial generation
│   ├── useClipboard.ts        #   Clipboard utilities
│   ├── useKeyValueTags.ts     #   Tag management
│   ├── usePinnedBots.ts       #   Pinned bots management
│   ├── useShikiHighlighter.ts #   Shiki syntax highlighter singleton
│   └── useTerminalCache.ts    #   Terminal output cache
├── constants/                 # Constants
│   ├── client-types.ts        #   LLM client type definitions
│   ├── compatibilities.ts     #   Feature compatibility flags
│   └── acl-presets.ts         #   ACL preset configurations
├── i18n/locales/              # Translation files (en.json, zh.json)
├── layout/
│   └── main-layout/           # Top-level layout (SidebarProvider)
├── lib/
│   └── api-client.ts          # SDK client setup (base URL, auth interceptor)
├── pages/                     # Route page components
│   ├── login/                 #   Login page
│   ├── main-section/          #   Chat section layout (bot sidebar + main container)
│   ├── settings-section/      #   Settings section layout (settings sidebar + KeepAlive)
│   ├── home/                  #   Chat interface (used by both `/` and `/chat/:botId?`)
│   │   ├── index.vue          #     Route ↔ store sync + workspace host (left shell lives in components/sidebar)
│   │   ├── composables/       #     Page-specific composables
│   │   │   ├── useFileManagerProvider.ts  # File manager context
│   │   │   └── useMediaGallery.ts         # Media gallery state
│   │   └── components/        #     Chat UI components
│   │       ├── chat-workspace.vue     # Dockview host: panel registry, theme, tab context menu, chat panel sync
│   │       ├── dockview/              # Dockview panel wrappers
│   │       │   ├── panel-chat.vue     #   Singleton chat panel (content follows active session)
│   │       │   ├── panel-file.vue     #   Opened-file panel (Monaco file viewer)
│   │       │   ├── panel-terminal.vue #   Terminal panel
│   │       │   ├── panel-browser.vue  #   Workspace browser panel (iframe, renderer 'always')
│   │       │   ├── panel-display.vue  #   Workspace desktop panel (WebRTC, renderer 'always')
│   │       │   ├── workspace-watermark.vue # Empty-dock watermark
│   │       │   ├── group-actions.vue  #   Group header "+" menu (new terminal/browser/desktop)
│   │       │   └── use-panel-visible.ts #  Panel visibility tracking composable
│   │       ├── chat-pane.vue          # Main chat area (messages, input, attachments)
│   │       ├── file-pane.vue          # Opened-file viewer pane
│   │       ├── terminal-pane.vue      # Interactive workspace terminal
│   │       ├── display-pane.vue       # Workspace desktop/browser display over WebRTC
│   │       ├── browser-pane.vue       # Embedded workspace browser pane
│   │       ├── session-info-panel.vue # Session info panel
│   │       ├── message-item.vue       # Single message (user/assistant, markdown, blocks)
│   │       ├── thinking-block.vue     # Collapsible thinking/reasoning block
│   │       ├── attachment-block.vue   # Attachment grid (images, audio, files)
│   │       ├── media-gallery-lightbox.vue  # Fullscreen media lightbox
│   │       ├── tool-call-block.vue    # Tool call wrapper (renders inline component)
│   │       ├── tool-call-inline.vue   # Inline tool call row: (icon) action target chevron
│   │       ├── tool-call-registry.ts  # Tool name → display (icon, action, target, detail)
│   │       ├── tool-call-detail-exec.vue    # Exec stdout/stderr/error detail
│   │       ├── tool-call-detail-edit.vue    # Edit old/new diff detail
│   │       ├── tool-call-detail-spawn.vue   # Spawn (subagent) task list + links
│   │       ├── tool-call-detail-image.vue   # generate_image preview
│   │       ├── tool-call-detail-generic.vue # Generic input/result JSON detail
│   │       └── schedule-trigger-block.vue  # Schedule trigger display
│   ├── bots/                  #   Bot list + detail (tabs: overview, desktop, container, memory, channels, etc.)
│   │   ├── index.vue          #     Bot grid
│   │   ├── new.vue            #     Create bot flow
│   │   ├── detail.vue         #     Bot detail with tabbed interface
│   │   └── components/        #     Bot sub-components
│   │       ├── bot-overview.vue       # Bot overview tab
│   │       ├── bot-settings.vue       # Bot settings tab
│   │       ├── bot-desktop.vue        # Workspace display/runtime tab
│   │       ├── bot-channels.vue       # Channel configuration tab
│   │       ├── bot-memory.vue         # Memory configuration tab
│   │       ├── bot-mcp.vue            # MCP connections tab
│   │       ├── bot-schedule.vue       # Schedule management tab
│   │       ├── bot-email.vue          # Email configuration tab
│   │       ├── bot-container.vue      # Container management tab
│   │       ├── bot-network.vue        # Workspace network tab
│   │       ├── bot-tool-approval.vue  # Tool approval settings tab
│   │       ├── bot-skills.vue         # Skills tab
│   │       ├── bot-access.vue         # Access control tab
│   │       ├── bot-compaction.vue     # Compaction settings tab
│   │       ├── bot-card.vue           # Bot card component
│   │       ├── model-select.vue       # Model selection dropdown
│   │       ├── model-options.vue      # Model options configuration
│   │       ├── reasoning-effort-select.vue  # Reasoning effort selector
│   │       ├── reasoning-effort.ts    # Reasoning effort constants
│   │       ├── search-provider-select.vue   # Search provider selector
│   │       ├── memory-provider-select.vue   # Memory provider selector
│   │       ├── tts-model-select.vue         # TTS model selector
│   │       ├── channel-settings-panel.vue   # Channel settings panel
│   │       ├── container-create-progress.vue # Container creation progress
│   │       └── weixin-qr-login.vue          # WeChat QR login
│   ├── providers/             #   LLM provider & model management
│   ├── web-search/            #   Web search provider management
│   ├── memory/                #   Memory provider management
│   ├── speech/                #   Legacy TTS page (redirects to voice)
│   ├── transcription/         #   Legacy transcription page (redirects to voice)
│   ├── voice/                 #   TTS + transcription provider management
│   ├── people/                #   User management (admin only)
│   ├── onboarding/            #   First-run setup wizard
│   ├── dev/components/        #   Dev-only component wall (see § Dev Component Wall)
│   ├── email/                 #   Email provider management
│   ├── supermarket/           #   Supermarket (template/skill marketplace)
│   ├── usage/                 #   Token usage statistics
│   ├── appearance/            #   Theme / language / appearance settings
│   ├── profile/               #   User profile settings (password)
│   ├── platform/              #   Platform management
│   ├── about/                 #   About page
│   └── oauth/                 #   OAuth callback pages
│       └── mcp-callback.vue   #     MCP OAuth callback handler
├── store/                     # Pinia stores
│   ├── user.ts                #   User state, JWT token, login/logout
│   ├── settings/              #   UI settings store (index.ts: theme, language; typography.ts)
│   ├── capabilities.ts        #   Server capabilities (container backend)
│   ├── chat-selection.ts      #   Current bot/session selection (localStorage persisted)
│   ├── chat-list.ts           #   Chat messages, streaming state, SSE/WS event processing
│   ├── workspace-tabs.ts      #   Chat/file/terminal/display tab state
│   ├── display-snapshots.ts   #   Latest display screenshots keyed by bot/session/tab
│   ├── bot-create-progress.ts #   Bot creation SSE progress state
│   ├── update.ts              #   Desktop/app update prompt state
│   └── chat-list.utils.ts     #   Chat list utility functions (+ chat-list.utils.test.ts)
├── stores/                    # Additional stores (non-core)
│   └── supermarket-mcp-draft.ts #  Supermarket MCP draft state
└── utils/                     # Utility functions
    ├── api-error.ts           #   API error message extraction
    ├── date-time.ts           #   Date/time formatting
    ├── date-time.test.ts      #   Date/time tests
    ├── channel-type-label.ts  #   Channel type label utilities
    ├── bot-workspace.ts       #   Container-vs-remote workspace detection helpers
    ├── display-snapshot.ts    #   Browser-safe display snapshot capture helpers
    ├── key-value-tags.ts      #   Tag ↔ Record conversion
    ├── key-value-tags.test.ts #   Tag conversion tests
    ├── image-ref.ts           #   Image reference URL resolution
    ├── image-ref.test.ts      #   Image ref tests
    ├── timezones.ts           #   Timezone list and utilities
    └── useControlVisibleStatus.ts  # Visibility control utility
```

## Routes

The app uses a two-section layout architecture:

### Chat Section (`/`)

| Path | Name | Component | Description |
|------|------|-----------|-------------|
| `/` | home | *(null stub)* | Home — empty state when no bot selected |
| `/bot/:botName?` | bot | *(null stub)* | Chat with optional bot name param; active session in Pinia/localStorage |
| `/chat/:botName?` | — | redirect | Legacy alias → `/bot/:botName?` |

Chat routes register **null stub components** in the router. The real UI (`MainSection`: sidebar + dockview) mounts **persistently in `App.vue`**, not via `<RouterView>`, so chat survives route changes into `/settings` without unmount/relayout (see § Layout System).

### Settings Section (`/settings`)

| Path | Name | Component | Description |
|------|------|-----------|-------------|
| `/settings/bots` | bots | `bots/index.vue` | Bot list grid |
| `/settings/bots/new` | bot-new | `bots/new.vue` | Create bot flow |
| `/settings/bots/new/progress` | bot-create-progress | `bots/new-progress.vue` | Bot creation progress (SSE) |
| `/settings/bots/:botName` | bot-detail | `bots/detail.vue` | Bot detail with tabs |
| `/settings/providers` | providers | `providers/index.vue` | LLM provider & model management |
| `/settings/web-search` | web-search | `web-search/index.vue` | Web search provider management |
| `/settings/memory` | memory | `memory/index.vue` | Memory provider management |
| `/settings/voice` | voice | `voice/index.vue` | TTS + transcription providers |
| `/settings/speech` | — | redirect | Legacy alias → `voice` |
| `/settings/transcription` | — | redirect | Legacy alias → `voice` |
| `/settings/email` | email | `email/index.vue` | Email provider management |
| `/settings/supermarket` | supermarket | `supermarket/index.vue` | Template/skill marketplace |
| `/settings/supermarket/skills/:registryId/:packageId` | supermarket-package-detail | `supermarket/package-detail.vue` | Registry Skill Package detail |
| `/settings/usage` | usage | `usage/index.vue` | Token usage statistics |
| `/settings/people` | people | `people/index.vue` | User management (admin only) |
| `/settings/appearance` | appearance | `appearance/index.vue` | Theme, locale, and appearance settings |
| `/settings/keyboard` | keyboard | `keyboard-shortcuts/index.vue` | Keyboard shortcut settings |
| `/settings/profile` | profile | `profile/index.vue` | User profile settings |
| `/settings/platform` | platform | `platform/index.vue` | Platform management |
| `/settings/about` | about | `about/index.vue` | About page |

`/settings` redirects to `/settings/bots` by default.

### Standalone Routes

| Path | Name | Component | Description |
|------|------|-----------|-------------|
| `/login` | Login | `login/index.vue` | Login form (no auth required) |
| `/onboarding` | onboarding | `onboarding/index.vue` | First-run setup wizard (redirects to `/` when complete) |
| `/oauth/mcp/callback` | oauth-mcp-callback | `oauth/mcp-callback.vue` | MCP OAuth callback (no auth required) |
| `/dev/components` | dev-components | `dev/components/index.vue` | Dev-only component wall (see below) |

### Auth Guard

- All routes except `/login` and `/oauth/*` require `localStorage.getItem('token')`.
- Logged-in users accessing `/login` are redirected to `/`.
- Incomplete onboarding redirects authenticated users to `/onboarding` (`router-guards/onboarding.ts`).
- Routes with `meta.adminOnly` (e.g. `/settings/people`) require `user.role === 'admin'`.
- Chunk load errors (dynamic import failures) trigger an automatic page reload.
- `/dev/*` is registered only in dev builds and requires `localStorage.setItem('memoh:dev-tools', '1')`.

## Layout System

The shell splits into three layers (`App.vue` is the orchestrator):

1. **Persistent chat area** — `MainSection` (`pages/main-section/`) mounts in `App.vue` whenever the route is chat (`home` / `bot`) **or** settings (`/settings/*`). It stays full-size while settings pages are active so dockview layout and message scroll survive route changes between chat and `/settings` (KeepAlive cannot do this; detaching the subtree caused relayout/flash regressions).

2. **Chat section internals** (`MainSection` — hand-rolled flex, no `MainLayout`):
   - On macOS desktop a 36px full-width drag strip clears the traffic lights; web has no strip.
   - **Sidebar** (`components/sidebar/`) — Activity rail (44px icon column: bot switcher avatar on top, Sessions/Files/Search views, Settings gear at bottom) + a resizable, collapsible side panel. Clicking the active rail icon toggles the panel open/closed. Files view is hidden without `workspace_read`.
   - **ChatWorkspace** (`pages/home/components/chat-workspace.vue`) — [dockview-vue](https://dockview.dev) host for the center area.

3. **Settings section** — `pages/settings-section/` renders in a fixed full-screen layer (visibility toggle, not v-if). Uses `MainLayout` with:
   - **SettingsSidebar** (`components/settings-sidebar/`) — Collapsible `/settings` route sidebar. Top has a "back to chat" button that restores the last selected bot/session. Menu items include Bots, Providers, Web Search, Memory, Voice, Email, Supermarket, Usage, People (admin), Appearance, Keyboard, Profile, Platform, and About.
   - **SidebarInset** — `<KeepAlive>` wrapped `<RouterView>` for settings pages.

4. **Auth-boundary pages** (`/login`, `/onboarding`, `/oauth/*`, `/dev/*`) — Neither `MainSection` nor the settings section mounts; `<RouterView>` renders them full-screen alone.

5. **Home/Chat content** (inside `MainSection` → `ChatWorkspace`):
   - Panel types: `chat` (singleton, content follows the active session), `file`, `terminal`, `browser`, `display`. Floating groups are disabled; panels use `renderer: 'always'` so terminals / iframes / WebRTC survive tab switches.
   - **ChatPane** — Message list with scroll and input area with attachments (KeepAlive-cached per session inside the chat panel).
   - **SessionInfoRing** — Session info display in the composer.

Several settings pages use **MasterDetailSidebarLayout** (`components/master-detail-sidebar-layout/`) for left-sidebar + detail-panel patterns (providers, web search, email, memory, voice).

### Dev Component Wall

`pages/dev/components/` is the living reference for `@felinic/ui` atoms and tokens. Reach it at `/dev/components` in dev builds after `localStorage.setItem('memoh:dev-tools', '1')`. Also openable via `Cmd/Ctrl+Shift+D` when dev tools are enabled. Use it to verify visual changes; `mise run lint` runs `scripts/check-ui-contract.mjs` as a mechanical guard.

## CSS & Theming

Design tokens, typography, elevation, and the atom-level visual contract live in `packages/ui/AGENTS.md` and `packages/ui/src/style.css`. **`packages/ui/AGENTS.md` is the enforcement contract** (backed by `scripts/check-ui-contract.mjs` in `mise run lint`). `packages/ui/DESIGN.md` retains supplementary visual specs. Read both skill + UI contract before making UI changes.

### Tailwind CSS 4

CSS-based configuration (no `tailwind.config.*` file). All design tokens (CSS variables, `@theme inline` mapping, base styles) live in `packages/ui/src/style.css`. The web app imports them via:

```css
@import "@felinic/ui/style.css";
```

### Dark Mode

- Runtime: `useColorMode` from `@vueuse/core` in `store/settings/index.ts`
- Storage: theme preference persisted via `useStorage`
- Toggle: Available in Settings page and login page
- Usage: semantic tokens auto-switch; no `dark:` prefix needed

### Styling Rules

- Use Tailwind utility classes; avoid `<style>` blocks.
- Always use semantic color tokens (`text-foreground`, `bg-card`, `border-border`, etc.) — never hardcode raw colors (`gray-*`, `bg-white`, `text-black`).
- Follow the design system rules in `packages/ui/AGENTS.md`.

## UI Components (@felinic/ui)

All UI primitives are provided by `@felinic/ui` (built on Reka UI). Do not import Reka UI directly. For the component design contract (variants, tokens, elevation, spacing), see `packages/ui/AGENTS.md`.

- **Exception**: Physical UI knobs (Switch thumb, Slider thumb) may keep `bg-white` as they need to contrast against colored tracks regardless of theme.
- **No scoped CSS modules**: Styling is done inline via utility classes.

### CSS Imports (main.ts)

```
style.css                    — Tailwind + theme tokens
animate.css                  — Animation utilities
markstream-vue/index.css     — Markdown rendering
katex/dist/katex.min.css     — Math rendering
```

Toast styling ships inside `@felinic/ui` `style.css` (the in-house `Toaster`); no separate toast stylesheet import is needed.

`@felinic/ui` provides 43 component groups built on Reka UI primitives + Tailwind + class-variance-authority:

- **Form**: `Form`, `FormField`, `FormFieldArray`, `FormItem`, `FormControl`, `FormLabel`, `FormMessage`, `FormDescription`
- **Input**: `Input`, `Textarea`, `InputGroup` (Addon, Button, Input, Text, Textarea), `NativeSelect`, `Combobox`, `TagsInput`, `InputOTP` (Group, Slot, Separator)
- **Selection**: `Select`, `RadioGroup`, `Checkbox`, `Switch`, `Toggle`, `Slider`
- **Layout**: `Card`, `Separator`, `Sheet`, `Sidebar` (24 sub-components), `ScrollArea`, `Collapsible`, `Item` (10 sub-components)
- **Overlays**: `Dialog` (incl. `DialogScrollContent`), `Popover`, `Tooltip`, `DropdownMenu`, `ContextMenu`, `Command` (Dialog, Group, Input, Item, List)
- **Data**: `Table` (9 sub-components), `Badge`, `BadgeCount`, `Avatar`, `Skeleton`, `Empty` (5 sub-components)
- **Navigation**: `Breadcrumb`, `Tabs`, `Pagination`, `PinInput` (Group, Slot, Separator)
- **Feedback**: `Button`, `ButtonGroup` (Separator, Text), `Spinner`, `Alert`, `Toaster` (in-house), `Kbd`
- **Effects**: `TextGenerateEffect`

### Form Pattern (vee-validate + Zod)

```vue
<script setup>
const form = useForm({
  validationSchema: toTypedSchema(z.object({
    name: z.string().min(1),
  })),
})
</script>

<template>
  <FormField v-slot="{ componentField }" name="name">
    <FormItem>
      <Label>Name</Label>
      <FormControl>
        <Input v-bind="componentField" />
      </FormControl>
      <FormMessage />
    </FormItem>
  </FormField>
</template>
```

### Icon Usage

- **Lucide** (primary): Direct component imports from `lucide-vue-next`. Example: `import { Plus, Search, Bot } from 'lucide-vue-next'` → `<Plus class="size-4" />`. Used for all UI icons (actions, navigation, status indicators, etc.).
- **`@memohai/icon`** (brand icons): Workspace package (`packages/icons/`) providing AI provider, search engine, and channel platform SVG icons as Vue components. Example: `import { Openai, Claude } from '@memohai/icon'`.
- **Do NOT use FontAwesome** for new code. Legacy FontAwesome usage remains only in commented-out code blocks. Always use Lucide for UI icons and `@memohai/icon` for brand logos.

### Notification Pattern

```typescript
import { toast } from '@felinic/ui'
toast.success(t('common.saved'))
toast.error(resolveApiErrorMessage(error, 'Failed'))
```

## Data Fetching

### API Client Setup (`lib/api-client.ts`)

- SDK: `@memohai/sdk` auto-generated from OpenAPI via `@hey-api/openapi-ts`
- Base URL: `VITE_API_URL` env var (defaults to `/api`, proxied by Vite dev server to backend)
- Auth: Request interceptor attaches `Authorization: Bearer ${token}` from localStorage
- 401 handling: Response interceptor removes token and redirects to `/login`

### Pinia Colada (Server State)

Primary data fetching mechanism for CRUD operations:

```typescript
// Query — auto-generated from SDK
const { data, isLoading } = useQuery(getBotsQuery())

// Custom query with dynamic key
const { data } = useQuery({
  key: () => ['bot-settings', botId.value],
  query: async () => {
    const { data } = await getBotsByBotIdSettings({
      path: { bot_id: botId.value },
      throwOnError: true,
    })
    return data
  },
  enabled: () => !!botId.value,
})

// Mutation with cache invalidation
const queryCache = useQueryCache()
const { mutateAsync } = useMutation({
  mutation: async (body) => {
    const { data } = await putBotsByBotIdSettings({
      path: { bot_id: botId.value },
      body,
      throwOnError: true,
    })
    return data
  },
  onSettled: () => queryCache.invalidateQueries({
    key: ['bot-settings', botId.value],
  }),
})
```

SDK also generates colada helpers: `getBotsQuery()`, `postBotsMutation()`, query key factories.

### Pinia Stores (Client State)

| Store | ID | Purpose |
|-------|----|---------|
| `user` | `user` | JWT token (`useLocalStorage`), user info (id, username, role, displayName, avatarUrl, timezone), login/logout |
| `settings` | `settings` | Theme (dark/light), language (en/zh), synced with `useColorMode` and vue-i18n locale |
| `capabilities` | `capabilities` | Server feature flags (container backend, snapshot support), loaded once from `getPing()` |
| `chat-selection` | `chat-selection` | Current bot ID and session ID, persisted via `useStorage` to localStorage |
| `chat-list` | `chat` | Chat messages, sessions, bots, streaming state, SSE/WS event processing. Depends on `chat-selection` store for current bot/session. Utility functions in `chat-list.utils.ts` |
| `workspace-tabs` | `workspace-tabs` | Dockview layout manager: holds the `DockviewApi` handle, opens/closes panels (chat/file/terminal/browser/display), serializes layout per bot under `workspace-layout`, and owns activity-bar/side-panel state (`sidebarView`, `sidebarOpen`, `sidebarWidth`). The active chat session lives in `chat-selection` |
| `display-snapshots` | `display-snapshots` | Last display screenshots for previews in chat and bot desktop settings |
| `bot-create-progress` | `bot-create-progress` | Bot creation SSE progress (used by `bots/new-progress.vue`) |
| `update` | `update` | Desktop/app update prompt state |

Additional stores in `stores/`:
| Store | Purpose |
|-------|---------|
| `supermarket-mcp-draft` | Supermarket MCP draft state management |

Stores use Composition API style (`defineStore(() => { ... })`), with persistence via `pinia-plugin-persistedstate` or `useStorage`.

### Streaming (Chat)

A conversation is read over the **WebSocket** only. SSE is left with one job that has nothing to do with conversation contents: telling the sidebar which sessions moved.

#### Sessions activity SSE
- **Endpoint**: `GET /bots/{bot_id}/sessions/events` — bot-wide lightweight activity stream; `session_touched` / `session_title_changed` / `session_created` for sidebar live-sort. Never carries message bodies.
- **Parsing**: handled by the generated SDK (`@memohai/sdk` `sse.get`); wrappers live in `composables/api/useChat.message-api.ts`.
- **Retry**: `useRetryingStream` composable drives reconnection with exponential backoff.
- There is no per-session SSE. A session's messages and run state come from the session runtime over the WebSocket, so that every subscriber of a session — this tab, another tab, another device — is reading the same projection instead of each building its own.

#### WebSocket
- **Endpoint**: `/bots/{bot_id}/local/ws` (with token query param)
- **Implementation**: `composables/api/useChat.ws.ts` wraps native `WebSocket` with send, abort, close, and auto-reconnect
- **State**: `store/chat-list.ts` processes streaming events from either transport into reactive message blocks in real-time
- **Abort**: Stream cancellation via `AbortSignal` (SSE) or close message (WS)

## Workspace, Display, Browser Use, and Computer Use

- The center workspace is a dockview layout managed by `store/workspace-tabs.ts`: chat / file / terminal / browser / display are dockview panels that can be tabbed together or split. The chat panel is a singleton that follows the active session; the workspace file browser lives in the left side panel (`components/sidebar/files-pane.vue`), not in the dock.
- Terminal and file panes are normal workspace features. Display panes are container-workspace features and are hidden while a bot is bound to a remote runtime.
- `pages/home/components/display-pane.vue` connects to the workspace display service, prepares the display runtime, opens a WebRTC session, forwards keyboard/pointer input, and captures snapshots for previews. It represents a headed container desktop with a browser, not a headless automation runner.
- `pages/bots/components/bot-desktop.vue` is the settings/runtime surface for enabling display, checking Xvnc/browser/toolkit availability, viewing live sessions, and closing display sessions.
- Agent Browser Use (`browser_action`, `browser_observe`, `browser_remote_session`) operates the headed workspace Chrome/Chromium instance exposed by the backend display stack. Computer Use is split across `computer_observe` (accessibility snapshot via the bundled `a11y-cli` helper, or a saved-to-path screenshot) and `computer_action` (ref-driven click/type/fill with raw RFB coordinates as fallback). Do not describe these as generic headless Playwright; headless Playwright remains a separate command-line workflow inside a workspace.

### Error Handling

- **Global**: `utils/api-error.ts` — `resolveApiErrorMessage()` extracts error from `message`, `error`, `detail` fields
- **Mutations**: `useDialogMutation` composable wraps mutations with automatic `toast.error()` on failure
- **SDK**: All calls use `throwOnError: true`; try/catch at component level
- **Streams**: `processing_failed` / `error` events appended to message blocks

## i18n

- Plugin: vue-i18n (Composition API, `legacy: false`)
- Locales: `en` (English, default), `zh` (Chinese)
- Files: `src/i18n/locales/en.json`, `src/i18n/locales/zh.json`
- Usage: `const { t } = useI18n()` → `t('bots.title')`
- Key namespaces: `common`, `auth`, `sidebar`, `breadcrumb`, `settings`, `about`, `chat`, `models`, `provider`, `webSearch`, `memory`, `speech`, `transcription`, `email`, `mcp`, `home`, `bots`, `usage`, `appearance`, `supermarket`

## Vite Configuration

- Dev server port: 8082 (from `config.toml`)
- Proxy: `/api` → backend (default `http://localhost:8080`)
- Aliases: `@` → `./src`, `#` → `../ui/src`
- Config: reads from `MEMOH_CONFIG_PATH` / `CONFIG_PATH` when provided, otherwise `../../config.toml`, via `@memohai/config`

## Development Rules

- **Read `packages/ui/AGENTS.md` first** for any UI or page work and follow the guidance it routes to.
- Use Vue 3 Composition API with `<script setup>` exclusively.
- Style with Tailwind utility classes; avoid `<style>` blocks. Follow `packages/ui/AGENTS.md` and the guidance it routes to.
- **Always use semantic color tokens** (`text-foreground`, `bg-card`, `border-border`, `text-muted-foreground`, `bg-accent`, etc.) instead of raw colors (`gray-*`, `bg-white`, `text-black`). Never introduce hardcoded Tailwind color classes for themed elements — this breaks dark mode consistency.
- Use `@felinic/ui` components for all UI primitives; do not import Reka UI directly.
- Use `lucide-vue-next` for all UI icons. Use `@memohai/icon` for brand/provider logos. **Never use FontAwesome** — do not add `<FontAwesomeIcon>`, do not import from `@fortawesome/*`, do not use inline SVG or base64-encoded SVG in templates.
- Use Pinia Colada (`useQuery`/`useMutation`) for server state; use Pinia stores for client state only.
- API calls must go through `@memohai/sdk`; never call `fetch()` directly.
- All user-facing strings must use i18n keys (`t('key')`) — never hardcode text.
- Forms must use vee-validate + Zod schemas via `toTypedSchema()`.
- Error messages via `resolveApiErrorMessage()` + `toast.error()`.
- Page components go in `pages/{feature}/`; page-specific sub-components go in `pages/{feature}/components/`.
- Page-specific composables go in `pages/{feature}/composables/`.
- Shared components go in `components/`.
- Composables go in `composables/`; API-specific composables in `composables/api/`.
