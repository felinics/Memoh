import { describe, expect, it } from 'vitest'
import type { AcpprofilePublicProfile } from '@memohai/sdk'
import {
  acpManagedFieldHelp,
  acpManagedFieldLabel,
} from './setup-fields'

const t = (key: string) => key

describe('generic ACP managed fields', () => {
  const generic: AcpprofilePublicProfile = {
    id: 'acp',
    display_name: 'ACP',
    setup_modes: ['api_key'],
    managed_fields: [
      { id: 'command', label: 'Command', required: true },
      { id: 'arguments', label: 'Arguments', type: 'textarea' },
    ],
  }

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
