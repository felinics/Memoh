import { describe, expect, it } from 'vitest'
import type { AcpprofilePublicProfile } from '@memohai/sdk'
import {
  acpManagedFieldHelp,
  acpManagedFieldLabel,
  acpSetupModeLabel,
  acpSetupModes,
  filterCreateVisibleManagedFields,
  filterSettingsVisibleManagedFields,
} from './setup-fields'

const t = (key: string) => key

function profile(overrides: Partial<AcpprofilePublicProfile> = {}): AcpprofilePublicProfile {
  return {
    id: 'codex',
    display_name: 'Codex',
    setup_modes: ['api_key', 'oauth', 'self'],
    managed_fields: [
      { id: 'api_key', label: 'API Key', required: true },
      { id: 'provider_id', label: 'Provider' },
      { id: 'oauth_token', label: 'OAuth' },
    ],
    ...overrides,
  }
}

describe('acpSetupModes', () => {
  it('falls back to api_key when setup_modes is empty', () => {
    expect(acpSetupModes(profile({ setup_modes: [] }))).toEqual(['api_key'])
  })
})

describe('acpSetupModeLabel', () => {
  it('labels codex oauth mode', () => {
    expect(acpSetupModeLabel(profile(), 'oauth', t)).toBe('bots.settings.acpSetupChatGPT')
  })
})

describe('generic ACP managed fields', () => {
  const generic = profile({
    id: 'acp',
    display_name: 'ACP',
    setup_modes: ['api_key'],
    managed_fields: [
      { id: 'command', label: 'Command', required: true },
      { id: 'arguments', label: 'Arguments', type: 'textarea' },
    ],
  })

  it('uses localized labels and help text', () => {
    const command = generic.managed_fields?.[0] ?? {}
    const argumentsField = generic.managed_fields?.[1] ?? {}
    expect(acpManagedFieldLabel(generic, command, t)).toBe('bots.settings.acpCommand')
    expect(acpManagedFieldHelp(generic, argumentsField, t)).toBe('bots.settings.acpArgumentsHelp')
  })
})

describe('filterCreateVisibleManagedFields', () => {
  it('returns no fields outside api_key mode', () => {
    expect(filterCreateVisibleManagedFields(profile(), {}, 'oauth')).toEqual([])
  })

  it('drops provider_id and oauth_token in api_key mode', () => {
    const fields = filterCreateVisibleManagedFields(profile(), {}, 'api_key')
    expect(fields.map(f => f.id)).toEqual(['api_key'])
  })
})

describe('filterSettingsVisibleManagedFields', () => {
  it('hides managed fields for codex oauth mode', () => {
    expect(filterSettingsVisibleManagedFields(profile(), {}, 'oauth')).toEqual([])
  })

  it('shows api_key field for claude in api_key mode only', () => {
    const claude = profile({ id: 'claude-code' })
    expect(filterSettingsVisibleManagedFields(claude, {}, 'api_key').map(f => f.id)).toEqual(['api_key'])
    expect(filterSettingsVisibleManagedFields(claude, {}, 'oauth').map(f => f.id)).toEqual([])
  })
})
