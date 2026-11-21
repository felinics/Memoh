import { isACPAgent, normalizeAgentID } from '@/utils/external-agent'
import type { AcpprofileManagedField, AcpprofilePublicProfile } from '@memohai/sdk'

type Translate = (key: string) => string

export function acpInputType(type: string | undefined): string {
  if (type === 'password') return 'password'
  if (type === 'url') return 'url'
  return 'text'
}

export function acpManagedFieldLabel(
  profile: AcpprofilePublicProfile,
  field: AcpprofileManagedField,
  t: Translate,
): string {
  if (isACPAgent(profile.id)) {
    const id = normalizeAgentID(field.id)
    if (id === 'command') return t('bots.settings.acpCommand')
    if (id === 'arguments') return t('bots.settings.acpArguments')
  }
  return field.label || field.id || ''
}

export function acpManagedFieldHelp(
  profile: AcpprofilePublicProfile,
  field: AcpprofileManagedField,
): string {
  // Command and Arguments say it themselves: the label names the field and the
  // placeholder (`my-agent-acp`, `--stdio`) shows the shape. A sentence under
  // each only restated that, on both the create panel and the settings page.
  if (isACPAgent(profile.id)) {
    const id = normalizeAgentID(field.id)
    if (id === 'command' || id === 'arguments') return ''
  }
  return field.help || ''
}

export function acpManagedPlaceholder(
  _profile: AcpprofilePublicProfile,
  field: AcpprofileManagedField,
): string | undefined {
  return field.placeholder
}

export function acpManagedFieldName(profile: AcpprofilePublicProfile, field: AcpprofileManagedField): string {
  return `acp-${normalizeAgentID(profile.id) || 'agent'}-${normalizeAgentID(field.id) || 'field'}`
}

export function acpManagedFieldAutocomplete(field: AcpprofileManagedField): string {
  return field.type === 'password' ? 'new-password' : 'off'
}
