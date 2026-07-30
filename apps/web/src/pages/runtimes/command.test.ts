import { describe, expect, it } from 'vitest'

import { buildRuntimeConnectCommand } from './command'

const key = `mrk_${'a'.repeat(64)}`
const teamId = '11111111-1111-4111-8111-111111111111'

describe('buildRuntimeConnectCommand', () => {
  it('includes the credential team ID required by hosted gateways', () => {
    expect(buildRuntimeConnectCommand('https://memoh.example/api', {
      key,
      team_id: teamId,
    })).toBe(
      `npx --yes @memohai/runtime --server https://memoh.example/api --key ${key} --team-id ${teamId}`,
    )
  })

  it('keeps credentials from older self-hosted servers usable', () => {
    expect(buildRuntimeConnectCommand('https://memoh.example/api', { key }))
      .toBe(`npx --yes @memohai/runtime --server https://memoh.example/api --key ${key}`)
  })

  it('enables plaintext WebSockets only for loopback development servers', () => {
    expect(buildRuntimeConnectCommand('http://127.0.0.1:18080', {
      key,
      team_id: teamId,
    })).toBe(
      `npx --yes @memohai/runtime --server http://127.0.0.1:18080 --key ${key} --team-id ${teamId} --insecure-localhost`,
    )
  })
})
