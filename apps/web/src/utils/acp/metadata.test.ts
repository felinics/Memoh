import { describe, expect, it } from 'vitest'
import type { AcpprofilePublicProfile } from '@memohai/sdk'
import {
  defaultSetupMode,
  findMissingRequiredManagedField,
  isACPAgentConfigured,
  readACPAgentForm,
  withACPAgentForm,
} from './metadata'

const genericProfile: AcpprofilePublicProfile = {
  id: 'acp',
  display_name: 'ACP',
  setup_modes: ['api_key'],
  managed_fields: [
    { id: 'command', label: 'Command', type: 'text', required: true },
    { id: 'arguments', label: 'Arguments', type: 'textarea' },
  ],
}

describe('ACP agent setup', () => {
  // The bug this guards: two custom ACP agents share the profile id "acp",
  // so anything keyed by profile made them read and write one setup.
  it('keeps two agents of the same profile apart', () => {
    const hermes = { metadata: { provider: 'acp', managed: { command: 'hermes-acp' } } }
    const grok = { metadata: { provider: 'acp', managed: { command: 'grok-acp', arguments: '--stdio' } } }

    expect(readACPAgentForm(hermes, genericProfile).managed).toEqual({ command: 'hermes-acp', arguments: '' })
    expect(readACPAgentForm(grok, genericProfile).managed).toEqual({ command: 'grok-acp', arguments: '--stdio' })

    const edited = readACPAgentForm(grok, genericProfile)
    edited.managed.command = 'grok-next'
    expect(withACPAgentForm(grok, edited)).toEqual({
      provider: 'acp',
      managed: { command: 'grok-next', arguments: '--stdio' },
    })
    expect(hermes.metadata.managed.command).toBe('hermes-acp')
  })

  it('starts a new agent with an empty setup', () => {
    const created = { metadata: { provider: 'acp', managed: {} } }

    expect(readACPAgentForm(created, genericProfile)).toEqual({
      setup_mode: 'api_key',
      managed: { command: '', arguments: '' },
    })
    expect(isACPAgentConfigured(created, genericProfile)).toBe(false)
  })

  it('reports an agent configured once its required fields are set', () => {
    expect(isACPAgentConfigured({ metadata: { provider: 'acp', managed: { command: 'hermes-acp' } } }, genericProfile)).toBe(true)
    expect(isACPAgentConfigured({ metadata: { provider: 'acp', managed: { command: '  ' } } }, genericProfile)).toBe(false)
    expect(isACPAgentConfigured({ metadata: { provider: 'acp', managed: { command: 'hermes-acp' } } }, null)).toBe(false)
  })

  it('ignores managed values the profile does not declare', () => {
    const agent = { metadata: { provider: 'acp', managed: { command: 'hermes-acp', stray: 'value', arguments: 42 } } }

    expect(readACPAgentForm(agent, genericProfile).managed).toEqual({ command: 'hermes-acp', arguments: '' })
  })
})

describe('findMissingRequiredManagedField', () => {
  it('returns the first required field left empty', () => {
    expect(findMissingRequiredManagedField(genericProfile, { command: '' }, 'api_key')?.id).toBe('command')
    expect(findMissingRequiredManagedField(genericProfile, { command: 'hermes-acp' }, 'api_key')).toBeNull()
  })

  it('requires nothing in self mode and rejects an unsupported mode', () => {
    const selfProfile: AcpprofilePublicProfile = { ...genericProfile, setup_modes: ['api_key', 'self'] }

    expect(findMissingRequiredManagedField(selfProfile, {}, 'self')).toBeNull()
    expect(findMissingRequiredManagedField(genericProfile, { command: 'hermes-acp' }, 'oauth')?.id).toBe('setup_mode')
  })
})

describe('defaultSetupMode', () => {
  it('takes the first mode the profile declares', () => {
    expect(defaultSetupMode({ ...genericProfile, setup_modes: ['oauth', 'api_key'] })).toBe('oauth')
    expect(defaultSetupMode({ ...genericProfile, setup_modes: [] })).toBe('api_key')
  })
})
