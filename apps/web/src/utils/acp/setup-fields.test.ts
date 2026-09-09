import { describe, expect, it } from 'vitest'
import type { AcpprofilePublicProfile } from '@memohai/sdk'
import {
  acpManagedFieldHelp,
  acpManagedFieldLabel,
  acpSetupModes,
  filterCreateVisibleManagedFields,
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

  it('uses localized labels', () => {
    const command = generic.managed_fields?.[0] ?? {}
    expect(acpManagedFieldLabel(generic, command, t)).toBe('bots.settings.acpCommand')
  })

  it('leaves command and arguments unexplained', () => {
    // The label plus the placeholder already carry it; help under these two was
    // a sentence restating the field name.
    const command = { ...(generic.managed_fields?.[0] ?? {}), help: 'from the profile' }
    const argumentsField = { ...(generic.managed_fields?.[1] ?? {}), help: 'from the profile' }
    expect(acpManagedFieldHelp(generic, command)).toBe('')
    expect(acpManagedFieldHelp(generic, argumentsField)).toBe('')
  })

  it('keeps profile-authored help on every other field', () => {
    expect(acpManagedFieldHelp(generic, { id: 'api_key', help: 'Paste your key' })).toBe('Paste your key')
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
