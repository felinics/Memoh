import { describe, expect, it } from 'vitest'

import packageMetadata from '../../../package.json' with { type: 'json' }
import { buildRuntimeConnectCommand, pinnedACPAdapterVersions } from './command'

const key = `mrk_${'a'.repeat(64)}`
const teamId = '11111111-1111-4111-8111-111111111111'
const runtimeEnvironment = [
  'npx --yes',
  `--package=@memohai/runtime@${packageMetadata.version}`,
  '--package=@agentclientprotocol/codex-acp@1.2.0',
  '--package=@agentclientprotocol/claude-agent-acp@0.66.0',
  '-- memoh-runtime',
].join(' ')

describe('buildRuntimeConnectCommand', () => {
  it('includes the credential team ID required by hosted gateways', () => {
    expect(buildRuntimeConnectCommand('https://memoh.example/api', {
      key,
      team_id: teamId,
    })).toBe(
      `${runtimeEnvironment} --server 'https://memoh.example/api' --key '${key}' --team-id '${teamId}'`,
    )
  })

  it('keeps credentials from older self-hosted servers usable', () => {
    expect(buildRuntimeConnectCommand('https://memoh.example/api', { key }))
      .toBe(`${runtimeEnvironment} --server 'https://memoh.example/api' --key '${key}'`)
  })

  it('enables plaintext WebSockets only for loopback development servers', () => {
    expect(buildRuntimeConnectCommand('http://127.0.0.1:18080', {
      key,
      team_id: teamId,
    })).toBe(
      `${runtimeEnvironment} --server 'http://127.0.0.1:18080' --key '${key}' --team-id '${teamId}' --insecure-localhost`,
    )
  })

  it('shell-quotes server-controlled values before invoking npx', () => {
    const serverUrl = 'https://memoh.example/api?label=o\'hare&next=$(id)'

    expect(buildRuntimeConnectCommand(serverUrl, { key }))
      .toContain('--server \'https://memoh.example/api?label=o\'\\\'\'hare&next=$(id)\'')
  })

  it('does not emit a partial command without a connection key', () => {
    expect(buildRuntimeConnectCommand('https://memoh.example/api', null)).toBe('')
    expect(buildRuntimeConnectCommand('https://memoh.example/api', { key: '   ' })).toBe('')
  })
})

describe('adapter version pins', () => {
  it('matches the versions Desktop bundles', async () => {
    const desktopManifest = await import('../../../../desktop/package.json')
    for (const [name, version] of Object.entries(pinnedACPAdapterVersions)) {
      expect(desktopManifest.dependencies[name as keyof typeof desktopManifest.dependencies]).toBe(version)
    }
  })
})
