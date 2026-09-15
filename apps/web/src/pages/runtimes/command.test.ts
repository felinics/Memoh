import { describe, expect, it } from 'vitest'

import { buildRuntimeConnectCommand, runtimeCliBin } from './command'

const key = `mrk_${'a'.repeat(64)}`
const teamId = '11111111-1111-4111-8111-111111111111'

describe('buildRuntimeConnectCommand', () => {
  it('installs the CLI and enrolls it as a background service', () => {
    expect(buildRuntimeConnectCommand('https://memoh.example/api', {
      key,
      team_id: teamId,
    })).toBe(
      `npm install -g @memohai/runtime && memoh-runtime enroll --server https://memoh.example/api --key ${key} --team-id ${teamId} && memoh-runtime service install && memoh-runtime service start`,
    )
  })

  it('keeps credentials from older self-hosted servers usable', () => {
    expect(buildRuntimeConnectCommand('https://memoh.example/api', { key }))
      .toBe(
        `npm install -g @memohai/runtime && memoh-runtime enroll --server https://memoh.example/api --key ${key} && memoh-runtime service install && memoh-runtime service start`,
      )
  })

  it('enables plaintext WebSockets only for loopback development servers', () => {
    expect(buildRuntimeConnectCommand('http://127.0.0.1:18080', {
      key,
      team_id: teamId,
    })).toBe(
      `npm install -g @memohai/runtime && memoh-runtime enroll --server http://127.0.0.1:18080 --key ${key} --team-id ${teamId} --insecure-localhost && memoh-runtime service install && memoh-runtime service start`,
    )
  })

  it('derives the hosted CLI bin from the hosted package name', () => {
    expect(buildRuntimeConnectCommand('https://memoh.example/api', { key }, '@memohai/cloud-runtime'))
      .toContain('npm install -g @memohai/cloud-runtime && memoh-cloud-runtime enroll')
  })
})

describe('runtimeCliBin', () => {
  it('follows the scoped package name convention', () => {
    expect(runtimeCliBin('@memohai/runtime')).toBe('memoh-runtime')
    expect(runtimeCliBin('@memohai/cloud-runtime')).toBe('memoh-cloud-runtime')
  })

  it('falls back to the OSS bin for unknown packages', () => {
    expect(runtimeCliBin('example.com/runtime-cli')).toBe('memoh-runtime')
  })
})
