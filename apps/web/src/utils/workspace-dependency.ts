import type { Component } from 'vue'
import { Bot, Layers, Package } from 'lucide-vue-next'
import { Anthropic, ClaudeCodeColor, CodexColor, Openai } from '@memohai/icon'
import type {
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

export type DependencyPrimaryActionKind = 'install' | 'reinstall' | 'retry' | 'align' | 'update' | 'viewProgress'

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

/** Installed agent whose version differs from the Server pin (design §10.1). */
export function dependencyNeedsAlignment(item: Pick<DependencyItem, 'category' | 'status' | 'needs_alignment'>): boolean {
  return item.category === 'agent' && item.status === 'installed' && item.needs_alignment === true
}

/** Installed tool whose last upstream check reported a newer version (design §10.2). */
export function dependencyUpdateAvailable(
  item: Pick<DependencyItem, 'category' | 'status' | 'update_available' | 'latest_version' | 'installed_version'>,
): boolean {
  if (item.category !== 'tool' || item.status !== 'installed') return false
  if (item.update_available === true) return true
  const latest = formatDependencyVersion(item.latest_version)
  return !!latest && latest !== formatDependencyVersion(item.installed_version)
}

/** Drives the tab count badge: rows the user should act on. */
export function dependencyNeedsAttention(item: DependencyItem): boolean {
  return item.status === 'missing'
    || item.status === 'failed'
    || dependencyNeedsAlignment(item)
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
  if (dependencyNeedsAlignment(item)) {
    return {
      variant: 'info',
      key: `${STATUS_KEY}.needsAlignment`,
      args: { version: formatDependencyVersion(item.required_version) },
      spinner: false,
    }
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
  const actions = item.actions ?? []
  if (actions.includes('reinstall') || actions.includes('update')) {
    if ((dependencyNeedsAlignment({ ...item, status: 'installed' })
      || dependencyUpdateAvailable({ ...item, status: 'installed' })) && actions.includes('update')) {
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
  if (dependencyPlatformUnsupported(item) || item.source === 'image') return null
  const disabled = workspaceState !== 'running'

  if (dependencyInProgress(item)) {
    // The badge already says "in progress"; only the client that holds the
    // stream can show its log, so other clients get no button at all.
    if (!options.ownsStream) return null
    return { kind: 'viewProgress', labelKey: `${ACTION_KEY}.viewProgress`, variant: 'outline', disabled: false }
  }
  if (item.status === 'failed') {
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
    return { kind: 'reinstall', labelKey: `${ACTION_KEY}.reinstall`, operation: 'install', variant: 'default', disabled }
  }
  if (!item.status) {
    return { kind: 'install', labelKey: `${ACTION_KEY}.install`, operation: 'install', variant: 'default', disabled }
  }
  if (dependencyNeedsAlignment(item)) {
    return { kind: 'align', labelKey: `${ACTION_KEY}.align`, operation: 'update', variant: 'default', disabled }
  }
  if (dependencyUpdateAvailable(item)) {
    return { kind: 'update', labelKey: `${ACTION_KEY}.update`, operation: 'update', variant: 'default', disabled }
  }
  return null
}

export function dependencyMenuActions(
  item: DependencyItem,
  workspaceState: DependencyWorkspaceState | undefined,
): DependencyMenuAction[] {
  const managed = item.source !== 'image'
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
    return managed ? [viewScript] : []
  }

  const readonly = workspaceState !== 'running' || dependencyInProgress(item)
  const items: DependencyMenuAction[] = []
  if (!managed) {
    // Visible but inert: the row description explains that the image provides
    // it, and a hidden item would leave the user hunting for "remove".
    items.push({
      kind: 'remove',
      labelKey: `${ACTION_KEY}.remove`,
      destructive: true,
      disabled: true,
      separatorBefore: false,
    })
    return items
  }

  if (item.status === 'installed' || item.status === 'failed') {
    items.push({
      kind: 'reinstall',
      labelKey: `${ACTION_KEY}.reinstall`,
      destructive: false,
      disabled: readonly,
      separatorBefore: false,
    })
  }
  const previous = formatDependencyVersion(item.previous_version)
  if (previous) {
    items.push({
      kind: 'rollback',
      labelKey: `${ACTION_KEY}.rollback`,
      args: { version: previous },
      destructive: false,
      disabled: readonly,
      separatorBefore: false,
    })
  }
  items.push({ ...viewScript, separatorBefore: items.length > 0 })
  if (item.status === 'installed' || item.status === 'missing' || item.status === 'failed') {
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
