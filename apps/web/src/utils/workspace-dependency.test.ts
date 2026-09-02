import { describe, expect, it } from 'vitest'
import { Bot, Layers, Package } from 'lucide-vue-next'
import { ClaudeCodeColor, CodexColor } from '@memohai/icon'
import type { DependencyItem } from '@/composables/api/useWorkspaceDependencies'
import {
  dependencyIcon,
  dependencyMenuActions,
  dependencyNeedsAttention,
  dependencyPrimaryAction,
  dependencyRetryOperation,
  dependencyStatusBadge,
  formatDependencyVersion,
} from './workspace-dependency'

function item(overrides: Partial<DependencyItem> = {}): DependencyItem {
  return {
    id: 'codex',
    name: 'Codex',
    category: 'agent',
    source: 'managed',
    platform_supported: true,
    provides: ['codex'],
    actions: [],
    ...overrides,
  }
}

describe('dependencyStatusBadge', () => {
  it('flags an unsupported platform before anything else', () => {
    const badge = dependencyStatusBadge(item({ platform_supported: false, status: 'installed', needs_alignment: true }))
    expect(badge).toMatchObject({ variant: 'outline', key: 'bots.dependencies.status.unsupported', spinner: false })
    expect(badge.tooltipKey).toBe('bots.dependencies.status.unsupportedTooltip')
  })

  it.each(['installing', 'updating', 'removing'] as const)('spins while %s', (status) => {
    expect(dependencyStatusBadge(item({ status }))).toEqual({
      variant: 'secondary',
      key: `bots.dependencies.status.${status}`,
      spinner: true,
    })
  })

  it('renders failed as destructive and missing as a reinstall warning', () => {
    expect(dependencyStatusBadge(item({ status: 'failed' }))).toMatchObject({ variant: 'destructive', key: 'bots.dependencies.status.failed' })
    expect(dependencyStatusBadge(item({ status: 'missing' }))).toMatchObject({ variant: 'warning', key: 'bots.dependencies.status.missing' })
  })

  it('asks an installed agent to align with the Server pin', () => {
    const badge = dependencyStatusBadge(item({
      status: 'installed',
      installed_version: '0.147.0',
      required_version: 'v0.151.0',
      needs_alignment: true,
    }))
    expect(badge).toEqual({
      variant: 'info',
      key: 'bots.dependencies.status.needsAlignment',
      args: { version: '0.151.0' },
      spinner: false,
    })
  })

  it('offers a tool update only from installed_version to a different latest_version', () => {
    const tool = item({ id: 'uv', category: 'tool', status: 'installed', installed_version: '0.5.0' })
    expect(dependencyStatusBadge({ ...tool, latest_version: '0.6.0' })).toMatchObject({
      variant: 'info',
      key: 'bots.dependencies.status.updateAvailable',
      args: { version: '0.6.0' },
    })
    expect(dependencyStatusBadge({ ...tool, latest_version: '0.5.0' })).toMatchObject({ variant: 'success' })
    expect(dependencyStatusBadge({ ...tool, update_available: true })).toMatchObject({ key: 'bots.dependencies.status.updateAvailable' })
  })

  it('ignores needs_alignment on a non-agent row and latest_version on an agent row', () => {
    expect(dependencyStatusBadge(item({ id: 'node', category: 'runtime', status: 'installed', needs_alignment: true })))
      .toMatchObject({ variant: 'success' })
    expect(dependencyStatusBadge(item({ status: 'installed', installed_version: '1', latest_version: '2' })))
      .toMatchObject({ variant: 'success', key: 'bots.dependencies.status.installed' })
  })

  it('falls back to not-installed when the catalog has no record', () => {
    expect(dependencyStatusBadge(item())).toEqual({
      variant: 'outline',
      key: 'bots.dependencies.status.notInstalled',
      spinner: false,
    })
  })
})

describe('dependencyPrimaryAction', () => {
  const running = 'running'

  it('installs a dependency without a record', () => {
    expect(dependencyPrimaryAction(item({ actions: ['install'] }), running)).toEqual({
      kind: 'install',
      labelKey: 'bots.dependencies.action.install',
      operation: 'install',
      variant: 'default',
      disabled: false,
    })
  })

  it('reinstalls a missing copy through the install script', () => {
    expect(dependencyPrimaryAction(item({ status: 'missing', actions: ['install', 'remove'] }), running)).toMatchObject({
      kind: 'reinstall',
      labelKey: 'bots.dependencies.action.reinstall',
      operation: 'install',
    })
  })

  it('retries a failed row with the operation the workspace state allows', () => {
    expect(dependencyPrimaryAction(item({ status: 'failed', actions: ['install', 'remove'] }), running)).toMatchObject({
      kind: 'retry',
      labelKey: 'common.retry',
      operation: 'install',
    })
    expect(dependencyRetryOperation(item({ status: 'failed', actions: ['update', 'reinstall', 'remove'] }))).toBe('reinstall')
    expect(dependencyRetryOperation(item({
      status: 'failed',
      needs_alignment: true,
      actions: ['update', 'reinstall', 'remove'],
    }))).toBe('update')
  })

  it('aligns an agent and updates a tool through the update operation', () => {
    expect(dependencyPrimaryAction(item({ status: 'installed', needs_alignment: true, actions: ['update'] }), running)).toMatchObject({
      kind: 'align',
      labelKey: 'bots.dependencies.action.align',
      operation: 'update',
    })
    expect(dependencyPrimaryAction(item({
      id: 'uv',
      category: 'tool',
      status: 'installed',
      installed_version: '1',
      latest_version: '2',
    }), running)).toMatchObject({ kind: 'update', labelKey: 'bots.dependencies.action.update', operation: 'update' })
  })

  it('shows progress only to the client that holds the stream', () => {
    const installing = item({ status: 'installing' })
    expect(dependencyPrimaryAction(installing, running)).toBeNull()
    expect(dependencyPrimaryAction(installing, running, { ownsStream: true })).toEqual({
      kind: 'viewProgress',
      labelKey: 'bots.dependencies.action.viewProgress',
      variant: 'outline',
      disabled: false,
    })
  })

  it('has no primary button for up-to-date, image-provided, or unsupported rows', () => {
    expect(dependencyPrimaryAction(item({ status: 'installed' }), running)).toBeNull()
    expect(dependencyPrimaryAction(item({ id: 'node', category: 'runtime', source: 'image' }), running)).toBeNull()
    expect(dependencyPrimaryAction(item({ platform_supported: false }), running)).toBeNull()
  })

  it.each(['not_running', 'missing', 'remote_offline', undefined] as const)('disables the button while the workspace is %s', (state) => {
    expect(dependencyPrimaryAction(item(), state)).toMatchObject({ kind: 'install', disabled: true })
  })
})

describe('dependencyMenuActions', () => {
  it('lists reinstall, rollback, script, and remove for an installed managed row', () => {
    const actions = dependencyMenuActions(item({ status: 'installed', previous_version: 'v0.147.0' }), 'running')
    expect(actions.map(action => action.kind)).toEqual(['reinstall', 'rollback', 'viewScript', 'remove'])
    expect(actions[1]).toMatchObject({ args: { version: '0.147.0' }, disabled: false })
    expect(actions[2]).toMatchObject({ separatorBefore: true, disabled: false })
    expect(actions[3]).toMatchObject({ destructive: true, disabled: false })
  })

  it('keeps only the script preview clickable while the workspace is read-only', () => {
    const actions = dependencyMenuActions(item({ status: 'installed', previous_version: '0.1.0' }), 'not_running')
    expect(actions.filter(action => !action.disabled).map(action => action.kind)).toEqual(['viewScript'])
  })

  it('offers script and remove for a missing row, script only for an uninstalled one', () => {
    expect(dependencyMenuActions(item({ status: 'missing' }), 'running').map(action => action.kind)).toEqual(['viewScript', 'remove'])
    expect(dependencyMenuActions(item(), 'running').map(action => action.kind)).toEqual(['viewScript'])
  })

  it('renders remove as a disabled item for image-provided rows', () => {
    expect(dependencyMenuActions(item({ id: 'node', category: 'runtime', source: 'image', status: 'installed' }), 'running')).toEqual([
      expect.objectContaining({ kind: 'remove', destructive: true, disabled: true }),
    ])
  })

  it('leaves only the script preview on an unsupported platform', () => {
    expect(dependencyMenuActions(item({ platform_supported: false, status: 'installed' }), 'running').map(action => action.kind)).toEqual(['viewScript'])
    expect(dependencyMenuActions(item({ platform_supported: false, source: 'image' }), 'running')).toEqual([])
  })
})

describe('dependencyNeedsAttention', () => {
  it('counts missing, failed, misaligned, and updatable rows', () => {
    expect(dependencyNeedsAttention(item({ status: 'missing' }))).toBe(true)
    expect(dependencyNeedsAttention(item({ status: 'failed' }))).toBe(true)
    expect(dependencyNeedsAttention(item({ status: 'installed', needs_alignment: true }))).toBe(true)
    expect(dependencyNeedsAttention(item({ id: 'uv', category: 'tool', status: 'installed', update_available: true }))).toBe(true)
    expect(dependencyNeedsAttention(item({ status: 'installed' }))).toBe(false)
    expect(dependencyNeedsAttention(item())).toBe(false)
  })
})

describe('dependencyIcon', () => {
  it('uses brand marks for the agents and category glyphs otherwise', () => {
    expect(dependencyIcon({ id: 'codex', category: 'agent' })).toBe(CodexColor)
    expect(dependencyIcon({ id: 'claude-code', category: 'agent' })).toBe(ClaudeCodeColor)
    expect(dependencyIcon({ id: 'custom', icon: 'codex', category: 'tool' })).toBe(CodexColor)
    expect(dependencyIcon({ id: 'hermes', category: 'agent' })).toBe(Bot)
    expect(dependencyIcon({ id: 'node', category: 'runtime' })).toBe(Layers)
    expect(dependencyIcon({ id: 'uv', category: 'tool' })).toBe(Package)
  })
})

describe('formatDependencyVersion', () => {
  it('trims and drops a leading v', () => {
    expect(formatDependencyVersion(' v22.12.0 ')).toBe('22.12.0')
    expect(formatDependencyVersion('0.151.0')).toBe('0.151.0')
    expect(formatDependencyVersion('version-1')).toBe('version-1')
    expect(formatDependencyVersion(undefined)).toBe('')
  })
})
