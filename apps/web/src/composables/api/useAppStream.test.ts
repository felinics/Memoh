import { describe, expect, it } from 'vitest'
import { isAppStreamEvent } from './useAppStream'
import { appLastError } from './useApps'

describe('isAppStreamEvent', () => {
  it('accepts a log event for an empty line, whose data the Server omits', () => {
    expect(isAppStreamEvent({ type: 'log', kind: 'dependency', id: 'document-node', stream: 'stderr' })).toBe(true)
    expect(isAppStreamEvent({ type: 'log', kind: 'dependency', id: 'document-node', stream: 'stderr', data: 1 })).toBe(false)
  })

  it('accepts a failed step carrying a catalog code', () => {
    expect(isAppStreamEvent({ type: 'step_done', kind: 'dependency', id: 'node', status: 'failed', code: 'workspace_dependency.busy' })).toBe(true)
    expect(isAppStreamEvent({ type: 'step_done', kind: 'dependency', id: 'node', status: 'failed', code: 1 })).toBe(false)
  })
})

describe('appLastError', () => {
  const translate = (key: string) => `t:${key}`

  it('renders the catalog copy when the row has a code, ignoring older text', () => {
    expect(appLastError({ last_error: 'old sentence', last_error_code: 'app.operation_failed' }, translate)).toBe('t:errors.app.operation_failed')
  })

  it('shows the text of an earlier server when the row has no code', () => {
    expect(appLastError({ last_error: ' old sentence ' }, translate)).toBe('old sentence')
    expect(appLastError({}, translate)).toBe('')
  })
})
