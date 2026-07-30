import { describe, expect, it } from 'vitest'

import { normalizeRuntimeTeamId, validateConfig } from '../src/config'

const key = `mrk_${'a'.repeat(64)}`
const teamId = '11111111-1111-4111-8111-111111111111'

describe('runtime config', () => {
  it('rejects unsafe URLs and relative workspace roots', () => {
    expect(() => validateConfig({ serverUrl: 'file:///tmp/socket', key, workspaceBase: '/tmp' })).toThrow('http')
    expect(() => validateConfig({ serverUrl: 'https://user:secret@example.test/api', key, workspaceBase: '/tmp' }))
      .toThrow('credentials')
    expect(() => validateConfig({ serverUrl: 'https://example.test/api', key, workspaceBase: 'relative' }))
      .toThrow('absolute')
  })

  it('normalizes and validates optional team IDs', () => {
    expect(normalizeRuntimeTeamId(` ${teamId.toUpperCase()} `)).toBe(teamId)
    expect(() => validateConfig({
      serverUrl: 'https://example.test/api',
      key,
      teamId,
      workspaceBase: '/tmp',
    })).not.toThrow()
    expect(() => validateConfig({
      serverUrl: 'https://example.test/api',
      key,
      teamId: 'not-a-team-id',
      workspaceBase: '/tmp',
    })).toThrow('UUID')
  })
})
