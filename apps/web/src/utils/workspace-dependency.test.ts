import { describe, expect, it } from 'vitest'
import { Package } from 'lucide-vue-next'
import { Anthropic, ClaudeCodeColor, CodexColor, Nodejs, Openai, Python, Uv } from '@memohai/icon'
import type { DependencyItem } from '@/composables/api/useWorkspaceDependencies'
import {
  dependencyIcon,
  dependencyIsInstalled,
  dependencyMenuActions,
  dependencyNeedsAttention,
  dependencyPrimaryAction,
  dependencyRetryOperation,
  dependencyStatusBadge,
  dependencyUpdateOperation,
  formatDependencyVersion,
  sortDependencies,
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

// The shape the Server reports for a copy the workspace image ships: installed,
// with install (an overlay) and check_update as the only actions.
function imageCopy(overrides: Partial<DependencyItem> = {}): DependencyItem {
  return item({
    id: 'node',
    name: 'Node.js',
    category: 'runtime',
    source: 'image',
    status: 'installed',
    installed_version: '24.14.0',
    image_version: '24.14.0',
    actions: ['install', 'check_update'],
    ...overrides,
  })
}

describe('dependencyStatusBadge', () => {
  it('flags an unsupported platform before anything else', () => {
    const badge = dependencyStatusBadge(item({ platform_supported: false, status: 'installed', update_available: true }))
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

  it('offers an update from installed_version to a different latest_version, whatever the category', () => {
    const tool = item({ id: 'uv', category: 'tool', status: 'installed', installed_version: '0.5.0' })
    expect(dependencyStatusBadge({ ...tool, latest_version: '0.6.0' })).toMatchObject({
      variant: 'info',
      key: 'bots.dependencies.status.updateAvailable',
      args: { version: '0.6.0' },
    })
    expect(dependencyStatusBadge({ ...tool, latest_version: '0.5.0' })).toMatchObject({ variant: 'success' })
    expect(dependencyStatusBadge({ ...tool, update_available: true })).toMatchObject({ key: 'bots.dependencies.status.updateAvailable' })
    expect(dependencyStatusBadge(item({ status: 'installed', installed_version: '0.150.0', latest_version: 'v0.151.0' })))
      .toMatchObject({ key: 'bots.dependencies.status.updateAvailable', args: { version: '0.151.0' } })
    expect(dependencyStatusBadge(imageCopy({ latest_version: '24.15.0' }))).toMatchObject({ key: 'bots.dependencies.status.updateAvailable' })
  })

  it('reads installed as installed whatever the copy comes from', () => {
    expect(dependencyStatusBadge(item({ status: 'installed', installed_version: '0.147.0' })))
      .toEqual({ variant: 'success', key: 'bots.dependencies.status.installed', spinner: false })
    expect(dependencyStatusBadge(imageCopy())).toMatchObject({ variant: 'success' })
    expect(dependencyStatusBadge(imageCopy({ id: 'codex', overlay: true, install_path: '/data/.memoh/deps/codex' }))).toMatchObject({ variant: 'success' })
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
      update_available: true,
      actions: ['update', 'reinstall', 'remove'],
    }))).toBe('update')
    expect(dependencyPrimaryAction(item({ status: 'failed', actions: [] }), running)).toBeNull()
  })

  it('updates any installed row with a newer version through the update operation', () => {
    const updatable: Partial<DependencyItem> = {
      status: 'installed',
      installed_version: '1',
      latest_version: '2',
      actions: ['update', 'reinstall', 'remove'],
    }
    expect(dependencyPrimaryAction(item(updatable), running)).toMatchObject({
      kind: 'update',
      labelKey: 'bots.dependencies.action.update',
      operation: 'update',
    })
    expect(dependencyPrimaryAction(item({ ...updatable, id: 'uv', category: 'tool' }), running)).toMatchObject({ kind: 'update' })
  })

  it('updates an image copy through an overlay install when the Server offers only install', () => {
    expect(dependencyPrimaryAction(imageCopy({ latest_version: '24.15.0' }), running)).toMatchObject({
      kind: 'update',
      labelKey: 'bots.dependencies.action.update',
      operation: 'install',
    })
    expect(dependencyUpdateOperation({ actions: ['install', 'check_update'] })).toBe('install')
    expect(dependencyUpdateOperation({ actions: ['update', 'install'] })).toBe('update')
    expect(dependencyUpdateOperation({ actions: ['reinstall'] })).toBeNull()
  })

  it('follows the Server action list and nothing else', () => {
    // No install listed → no button, even without a record.
    expect(dependencyPrimaryAction(item({ actions: [] }), running)).toBeNull()
    expect(dependencyPrimaryAction(item({ status: 'missing', actions: ['remove'] }), running)).toBeNull()
    expect(dependencyPrimaryAction(item({ status: 'installed', installed_version: '1', latest_version: '2', actions: ['reinstall'] }), running))
      .toBeNull()
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

  it('has no primary button for up-to-date or unsupported rows', () => {
    expect(dependencyPrimaryAction(item({ status: 'installed', actions: ['update', 'reinstall', 'remove'] }), running)).toBeNull()
    expect(dependencyPrimaryAction(imageCopy(), running)).toBeNull()
    expect(dependencyPrimaryAction(item({ platform_supported: false, actions: ['install'] }), running)).toBeNull()
  })

  it.each(['not_running', 'missing', 'remote_offline', undefined] as const)('disables the button while the workspace is %s', (state) => {
    expect(dependencyPrimaryAction(item({ actions: ['install'] }), state)).toMatchObject({ kind: 'install', disabled: true })
  })
})

describe('dependencyMenuActions', () => {
  it('lists reinstall, rollback, script, and remove for an installed managed row', () => {
    const actions = dependencyMenuActions(item({
      status: 'installed',
      previous_version: 'v0.147.0',
      actions: ['update', 'reinstall', 'remove', 'rollback'],
    }), 'running')
    expect(actions.map(action => action.kind)).toEqual(['reinstall', 'rollback', 'viewScript', 'remove'])
    expect(actions[1]).toMatchObject({ args: { version: '0.147.0' }, disabled: false })
    expect(actions[2]).toMatchObject({ separatorBefore: true, disabled: false })
    expect(actions[3]).toMatchObject({ destructive: true, disabled: false })
  })

  it('offers install and script for an up-to-date image copy the Server lets you lay a version over', () => {
    const actions = dependencyMenuActions(imageCopy(), 'running')
    expect(actions.map(action => action.kind)).toEqual(['install', 'viewScript'])
    expect(actions[0]).toMatchObject({ labelKey: 'bots.dependencies.action.install', disabled: false })
    // Once install doubles as the row's Update button, the menu does not repeat it.
    expect(dependencyMenuActions(imageCopy({ latest_version: '24.15.0' }), 'running').map(action => action.kind)).toEqual(['viewScript'])
  })

  it('keeps only the script preview clickable while the workspace is read-only', () => {
    const actions = dependencyMenuActions(item({
      status: 'installed',
      previous_version: '0.1.0',
      actions: ['update', 'reinstall', 'remove', 'rollback'],
    }), 'not_running')
    expect(actions.filter(action => !action.disabled).map(action => action.kind)).toEqual(['viewScript'])
    expect(dependencyMenuActions(imageCopy(), 'not_running').filter(action => !action.disabled).map(action => action.kind)).toEqual(['viewScript'])
  })

  it('offers script and remove for a missing row, script only for an uninstalled one', () => {
    expect(dependencyMenuActions(item({ status: 'missing', actions: ['install', 'remove'] }), 'running').map(action => action.kind))
      .toEqual(['viewScript', 'remove'])
    expect(dependencyMenuActions(item({ actions: ['install'] }), 'running').map(action => action.kind)).toEqual(['viewScript'])
  })

  it('keeps the script preview while an operation empties the action list', () => {
    expect(dependencyMenuActions(item({ status: 'installing', actions: [] }), 'running').map(action => action.kind)).toEqual(['viewScript'])
  })

  it('hides rollback until the Server lists it, even with a previous version recorded', () => {
    const kinds = dependencyMenuActions(item({ status: 'installed', previous_version: '0.1.0', actions: ['update', 'reinstall', 'remove'] }), 'running')
      .map(action => action.kind)
    expect(kinds).toEqual(['reinstall', 'viewScript', 'remove'])
  })

  it('has no menu at all when the Server lists no scripted action', () => {
    expect(dependencyMenuActions(item({ status: 'installed', actions: ['check_update'] }), 'running')).toEqual([])
    expect(dependencyMenuActions(item({ status: 'installed', actions: [] }), 'running')).toEqual([])
  })

  it('leaves only the script preview on an unsupported platform', () => {
    expect(dependencyMenuActions(item({ platform_supported: false, status: 'installed', actions: ['update'] }), 'running').map(action => action.kind)).toEqual(['viewScript'])
    expect(dependencyMenuActions(item({ platform_supported: false }), 'running')).toEqual([])
  })
})

describe('dependencyNeedsAttention', () => {
  it('counts missing, failed, and updatable rows', () => {
    expect(dependencyNeedsAttention(item({ status: 'missing' }))).toBe(true)
    expect(dependencyNeedsAttention(item({ status: 'failed' }))).toBe(true)
    expect(dependencyNeedsAttention(item({ status: 'installed', update_available: true }))).toBe(true)
    expect(dependencyNeedsAttention(imageCopy({ update_available: true }))).toBe(true)
    expect(dependencyNeedsAttention(item({ status: 'installed' }))).toBe(false)
    expect(dependencyNeedsAttention(imageCopy())).toBe(false)
    expect(dependencyNeedsAttention(item())).toBe(false)
  })
})

describe('dependencyIsInstalled', () => {
  it('lists rows with a record or a copy in effect, never a bare catalog entry', () => {
    expect(dependencyIsInstalled(item({ status: 'installed' }))).toBe(true)
    expect(dependencyIsInstalled(item({ status: 'failed' }))).toBe(true)
    expect(dependencyIsInstalled(item({ status: 'missing' }))).toBe(true)
    expect(dependencyIsInstalled(imageCopy({ status: undefined }))).toBe(true)
    expect(dependencyIsInstalled(item())).toBe(false)
    expect(dependencyIsInstalled(item({ installed_version: '  ' }))).toBe(false)
  })
})

describe('sortDependencies', () => {
  it('puts rows that need a hand first, then sorts by name regardless of category', () => {
    const sorted = sortDependencies([
      imageCopy({ id: 'uv', name: 'uv' }),
      item({ id: 'codex', name: 'Codex', status: 'installed' }),
      imageCopy({ id: 'python', name: 'Python', latest_version: '3.15.0' }),
      item({ id: 'claude-code', name: 'Claude Code', status: 'failed' }),
      imageCopy(),
    ])
    expect(sorted.map(entry => entry.id)).toEqual(['claude-code', 'python', 'codex', 'node', 'uv'])
  })

  it('sorts by the caller\'s display name and leaves the input untouched', () => {
    const input = [item({ id: 'b', name: 'B' }), item({ id: 'a', name: 'A' })]
    expect(sortDependencies(input, entry => entry.id === 'b' ? 'Alpha' : 'Beta').map(entry => entry.id)).toEqual(['b', 'a'])
    expect(input.map(entry => entry.id)).toEqual(['b', 'a'])
  })
})

describe('dependencyIcon', () => {
  it('maps the catalog icon identifier to the brand mark, id as fallback', () => {
    expect(dependencyIcon({ id: 'codex', icon: 'openai' })).toBe(Openai)
    expect(dependencyIcon({ id: 'claude-code', icon: 'anthropic' })).toBe(Anthropic)
    expect(dependencyIcon({ id: 'node', icon: 'nodejs' })).toBe(Nodejs)
    expect(dependencyIcon({ id: 'python', icon: 'python' })).toBe(Python)
    expect(dependencyIcon({ id: 'uv', icon: 'uv' })).toBe(Uv)
    expect(dependencyIcon({ id: 'codex' })).toBe(CodexColor)
    expect(dependencyIcon({ id: 'claude-code' })).toBe(ClaudeCodeColor)
    expect(dependencyIcon({ id: 'node' })).toBe(Nodejs)
    expect(dependencyIcon({ id: 'custom', icon: 'codex' })).toBe(CodexColor)
  })

  it('falls back to the package glyph for unknown identifiers', () => {
    expect(dependencyIcon({ id: 'hermes' })).toBe(Package)
    expect(dependencyIcon({ id: 'custom', icon: 'something-else' })).toBe(Package)
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
