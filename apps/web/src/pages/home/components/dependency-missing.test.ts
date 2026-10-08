import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'
import {
  dependencyInstallationInProgress,
  dependencyMissingArgs,
  dependencyMissingText,
  isDependencyMissingBlock,
} from './dependency-missing'

describe('isDependencyMissingBlock', () => {
  it('matches only the missing-dependency error code', () => {
    expect(isDependencyMissingBlock({ id: 0, type: 'error', code: 'agent_dependency_missing', content: '' })).toBe(true)
    expect(isDependencyMissingBlock({ id: 0, type: 'error', code: ' agent_dependency_missing ', content: '' })).toBe(true)
    expect(isDependencyMissingBlock({ id: 0, type: 'error', code: 'external_runtime_unavailable', content: '' })).toBe(false)
    // A notice carrying the code is not a rejection; it stays a plain notice.
    expect(isDependencyMissingBlock({ id: 0, type: 'notice', name: 'agent_dependency_missing', content: 'x' })).toBe(false)
    expect(isDependencyMissingBlock({ id: 0, type: 'text', content: 'hi' })).toBe(false)
  })
})

describe('dependencyMissingArgs', () => {
  it('keeps trimmed non-empty string args', () => {
    expect(dependencyMissingArgs({
      id: 0,
      type: 'error',
      code: 'agent_dependency_missing',
      content: '',
      args: { dep_id: ' codex ', install_task_id: ' task-1 ', request_id: '' },
    })).toEqual({ dep_id: 'codex', install_task_id: 'task-1' })
    expect(dependencyMissingArgs({ id: 0, type: 'error', content: 'x' })).toEqual({})
  })
})

describe('dependencyInstallationInProgress', () => {
  it('does not promise installation without an accepted operation', () => {
    expect(dependencyInstallationInProgress({ dep_id: 'codex' })).toBe(false)
    expect(dependencyInstallationInProgress({ operation_in_progress: 'false' })).toBe(false)
    expect(dependencyInstallationInProgress({ operation_in_progress: 'true' })).toBe(true)
    expect(dependencyInstallationInProgress({ install_task_id: 'task-1' })).toBe(true)
  })
})

describe('dependencyMissingText', () => {
  const { t, te } = createI18n({ legacy: false, locale: 'en', messages: { en } }).global
  const block = (args?: Record<string, string>) => ({
    id: 0,
    type: 'error' as const,
    code: 'agent_dependency_missing',
    content: '',
    args,
  })

  it('names the missing dependency from the block args', () => {
    expect(dependencyMissingText(block({ dep_id: 'codex' }), t, te))
      .toBe(en.errors.agent_dependency_missing.replace('{dep_id}', 'codex'))
  })

  it('says the dependency is installing while an accepted install runs', () => {
    expect(dependencyMissingText(block({ dep_id: 'codex', install_task_id: 'task-1' }), t, te))
      .toBe(en.chat.externalAgent.dependencyMissing.replace('{dep_id}', 'codex'))
  })

  it('shows the copy for the code for a block without args', () => {
    const text = dependencyMissingText(block(), t, te)

    expect(text).toContain(en.errors.agent_dependency_missing.replace('{dep_id} ', ''))
  })
})
