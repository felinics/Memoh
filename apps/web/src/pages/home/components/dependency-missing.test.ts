import { describe, expect, it } from 'vitest'
import { dependencyMissingArgs, isDependencyMissingBlock } from './dependency-missing'

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
