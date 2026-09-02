import type { Component } from 'vue'
import { Bot, Layers, Package } from 'lucide-vue-next'
import { Anthropic, ClaudeCodeColor, CodexColor, Openai } from '@memohai/icon'
import type {
  DependencyAvailableAction,
  DependencyItem,
  DependencyOperationAction,
  DependencyWorkspaceState,
} from '@/composables/api/useWorkspaceDependencies'

// Pure display rules for one dependency row, shared by the panel, the enable
// flow, and the tab badge. Everything here is derived from the Server's item
// plus the list's workspace_state; nothing reads component state, so the state
// table below can be unit-tested against the design's wording.

export type DependencyBadgeVariant = 'outline' | 'secondary' | 'destructive' | 'warning' | 'info' | 'success'

export interface DependencyStatusBadge {
  variant: DependencyBadgeVariant
  /** Full i18n key (`bots.dependencies.status.*`). */
  key: string
  args?: Record<string, string>
  /** In-progress states render a small Spinner inside the badge. */
  spinner: boolean
  /** Full i18n key of the tooltip explaining the state (unsupported only). */
  tooltipKey?: string
}

export type DependencyPrimaryActionKind = 'install' | 'reinstall' | 'retry' | 'update' | 'viewProgress'

export interface DependencyPrimaryAction {
  kind: DependencyPrimaryActionKind
  /** Full i18n key of the button label. */
  labelKey: string
  /** The streamed operation the button starts; `viewProgress` starts none. */
  operation?: DependencyOperationAction
  variant: 'default' | 'outline'
  /** Read-only workspace (not running / missing / offline) or unsupported platform. */
  disabled: boolean
}

export type DependencyMenuActionKind = 'reinstall' | 'rollback' | 'viewScript' | 'remove'

export interface DependencyMenuAction {
  kind: DependencyMenuActionKind
  /** Full i18n key of the item label. */
  labelKey: string
  args?: Record<string, string>
  destructive: boolean
  disabled: boolean
  /** Renders a DropdownMenuSeparator above the item. */
  separatorBefore: boolean
}

export type DependencyConfirmMode = 'install' | 'update' | 'align' | 'reinstall'

export type DependencyProgressStatus = 'running' | 'done' | 'error'

export interface DependencyLogLine {
  /** Stable key for rendering; callers may fall back to the index. */
  id?: string | number
  /** `system` is a client-side line (e.g. the `started` event), styled like stderr. */
  stream: 'stdout' | 'stderr' | 'system'
  data: string
}

const STATUS_KEY = 'bots.dependencies.status'
const ACTION_KEY = 'bots.dependencies.action'

export function dependencyDisplayName(item: Pick<DependencyItem, 'id' | 'name'>): string {
  return item.name?.trim() || item.id?.trim() || ''
}

/** Normalizes a version for display: trimmed, without a leading `v`. */
export function formatDependencyVersion(version: string | undefined | null): string {
  const trimmed = (version ?? '').trim()
  return trimmed.replace(/^[vV](?=\d)/, '')
}

export function dependencyInProgress(item: Pick<DependencyItem, 'status'>): boolean {
  return item.status === 'installing' || item.status === 'updating' || item.status === 'removing'
}

export function dependencyPlatformUnsupported(item: Pick<DependencyItem, 'platform_supported'>): boolean {
  return item.platform_supported === false
}

/**
 * Installed row whose last upstream check reported a newer version. No
 * dependency is pinned by the Server any more — agents and tools alike install
 * the latest and can be updated or rolled back — so the rule is the same for
 * every category and never reads `required_version` / `needs_alignment`.
 */
export function dependencyUpdateAvailable(
  item: Pick<DependencyItem, 'status' | 'update_available' | 'latest_version' | 'installed_version'>,
): boolean {
  if (item.status !== 'installed') return false
  if (item.update_available === true) return true
  const latest = formatDependencyVersion(item.latest_version)
  return !!latest && latest !== formatDependencyVersion(item.installed_version)
}

/**
 * Whether the Server lets this action be requested right now (`actions`).
 * Buttons key off this list, never off `source`: an image-provided row gets
 * no buttons today only because the Server lists nothing for it, and will
 * grow an install the day the Server does.
 */
export function dependencyAllows(item: Pick<DependencyItem, 'actions'>, action: DependencyAvailableAction): boolean {
  return (item.actions ?? []).includes(action)
}

/** Drives the tab count badge: rows the user should act on. */
export function dependencyNeedsAttention(item: DependencyItem): boolean {
  return item.status === 'missing'
    || item.status === 'failed'
    || dependencyUpdateAvailable(item)
}

export function dependencyStatusBadge(item: DependencyItem): DependencyStatusBadge {
  if (dependencyPlatformUnsupported(item)) {
    return {
      variant: 'outline',
      key: `${STATUS_KEY}.unsupported`,
      spinner: false,
      tooltipKey: `${STATUS_KEY}.unsupportedTooltip`,
    }
  }
  if (dependencyInProgress(item)) {
    return { variant: 'secondary', key: `${STATUS_KEY}.${item.status}`, spinner: true }
  }
  if (item.status === 'failed') {
    return { variant: 'destructive', key: `${STATUS_KEY}.failed`, spinner: false }
  }
  if (item.status === 'missing') {
    return { variant: 'warning', key: `${STATUS_KEY}.missing`, spinner: false }
  }
  if (dependencyUpdateAvailable(item)) {
    return {
      variant: 'info',
      key: `${STATUS_KEY}.updateAvailable`,
      args: { version: formatDependencyVersion(item.latest_version) },
      spinner: false,
    }
  }
  if (item.status === 'installed') {
    return { variant: 'success', key: `${STATUS_KEY}.installed`, spinner: false }
  }
  return { variant: 'outline', key: `${STATUS_KEY}.notInstalled`, spinner: false }
}

/**
 * The operation a "Retry" on a failed row replays. The Server keeps no
 * last-operation column, so this follows what the workspace still holds: a
 * present copy retries as an update (when one is due) or a reinstall, an
 * absent copy retries the install.
 */
export function dependencyRetryOperation(item: DependencyItem): DependencyOperationAction {
  if (dependencyAllows(item, 'reinstall') || dependencyAllows(item, 'update')) {
    if (dependencyUpdateAvailable({ ...item, status: 'installed' }) && dependencyAllows(item, 'update')) {
      return 'update'
    }
    return 'reinstall'
  }
  return 'install'
}

export function dependencyPrimaryAction(
  item: DependencyItem,
  workspaceState: DependencyWorkspaceState | undefined,
  options: { ownsStream?: boolean } = {},
): DependencyPrimaryAction | null {
  if (dependencyPlatformUnsupported(item)) return null
  const disabled = workspaceState !== 'running'

  if (dependencyInProgress(item)) {
    // The badge already says "in progress"; only the client that holds the
    // stream can show its log, so other clients get no button at all.
    if (!options.ownsStream) return null
    return { kind: 'viewProgress', labelKey: `${ACTION_KEY}.viewProgress`, variant: 'outline', disabled: false }
  }
  if (item.status === 'failed') {
    if (!item.actions?.length) return null
    return {
      kind: 'retry',
      labelKey: 'common.retry',
      operation: dependencyRetryOperation(item),
      variant: 'default',
      disabled,
    }
  }
  if (item.status === 'missing') {
    // Reads "reinstall" (the record remembers it was installed) but runs the
    // install script: nothing is left in the workspace to remove first.
    if (!dependencyAllows(item, 'install')) return null
    return { kind: 'reinstall', labelKey: `${ACTION_KEY}.reinstall`, operation: 'install', variant: 'default', disabled }
  }
  if (!item.status) {
    if (!dependencyAllows(item, 'install')) return null
    return { kind: 'install', labelKey: `${ACTION_KEY}.install`, operation: 'install', variant: 'default', disabled }
  }
  if (dependencyUpdateAvailable(item) && dependencyAllows(item, 'update')) {
    return { kind: 'update', labelKey: `${ACTION_KEY}.update`, operation: 'update', variant: 'default', disabled }
  }
  return null
}

const SCRIPTED_ACTIONS: DependencyAvailableAction[] = ['install', 'update', 'reinstall', 'remove']

export function dependencyMenuActions(
  item: DependencyItem,
  workspaceState: DependencyWorkspaceState | undefined,
): DependencyMenuAction[] {
  // A row backed by catalog scripts can always show them — including while an
  // operation runs and `actions` is empty. Image-provided rows have no scripts
  // until the Server lists a scripted action for them.
  const scripted = item.source !== 'image' || SCRIPTED_ACTIONS.some(action => dependencyAllows(item, action))
  // Script preview is rendered by the Server from the catalog; it needs no
  // workspace, so it stays clickable while everything else is read-only.
  const viewScript: DependencyMenuAction = {
    kind: 'viewScript',
    labelKey: `${ACTION_KEY}.viewScript`,
    destructive: false,
    disabled: false,
    separatorBefore: false,
  }
  if (dependencyPlatformUnsupported(item)) {
    return scripted ? [viewScript] : []
  }

  const readonly = workspaceState !== 'running' || dependencyInProgress(item)
  const items: DependencyMenuAction[] = []
  if (dependencyAllows(item, 'reinstall')) {
    items.push({
      kind: 'reinstall',
      labelKey: `${ACTION_KEY}.reinstall`,
      destructive: false,
      disabled: readonly,
      separatorBefore: false,
    })
  }
  const previous = formatDependencyVersion(item.previous_version)
  if (previous && dependencyAllows(item, 'rollback')) {
    items.push({
      kind: 'rollback',
      labelKey: `${ACTION_KEY}.rollback`,
      args: { version: previous },
      destructive: false,
      disabled: readonly,
      separatorBefore: false,
    })
  }
  if (scripted) items.push({ ...viewScript, separatorBefore: items.length > 0 })
  if (dependencyAllows(item, 'remove')) {
    items.push({
      kind: 'remove',
      labelKey: `${ACTION_KEY}.remove`,
      destructive: true,
      disabled: readonly,
      separatorBefore: false,
    })
  }
  return items
}

/**
 * Brand mark when the catalog names one (`icon`, else the id), otherwise a
 * lucide glyph per category. `@memohai/icon` ships no node / python / uv marks,
 * so runtimes and tools deliberately fall back to the neutral glyph.
 */
export function dependencyIcon(item: Pick<DependencyItem, 'id' | 'icon' | 'category'>): Component {
  const key = (item.icon?.trim() || item.id?.trim() || '').toLowerCase()
  switch (key) {
    case 'codex':
      return CodexColor
    case 'claude-code':
      return ClaudeCodeColor
    case 'openai':
      return Openai
    case 'anthropic':
      return Anthropic
  }
  switch (item.category) {
    case 'agent':
      return Bot
    case 'runtime':
      return Layers
    default:
      return Package
  }
}
