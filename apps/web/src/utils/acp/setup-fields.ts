import type { AcpprofileManagedField, AcpprofilePublicProfile } from '@memohai/sdk'
import { isACPAgent } from './agent-icon'
import { normalizeACPAgentID } from './metadata'

export type AcpSetupModeLabelTranslate = (key: string) => string

export function acpSetupModes(profile: AcpprofilePublicProfile): string[] {
  const modes = (profile.setup_modes ?? []).filter(Boolean)
  return modes.length > 0 ? modes : ['api_key']
}

export function acpSetupModeLabel(
  _profile: AcpprofilePublicProfile,
  mode: string,
  t: AcpSetupModeLabelTranslate,
): string {
  if (mode === 'api_key') return t('bots.settings.acpSetupApiKey')
  if (mode === 'oauth') return t('bots.settings.acpSetupOAuth')
  if (mode === 'self') return t('bots.settings.acpSetupSelf')
  return mode
}

export function acpInputType(type: string | undefined): string {
  if (type === 'password') return 'password'
  if (type === 'url') return 'url'
  return 'text'
}

export function acpManagedFieldLabel(
  profile: AcpprofilePublicProfile,
  field: AcpprofileManagedField,
  t: AcpSetupModeLabelTranslate,
): string {
  if (isACPAgent(profile.id)) {
    const id = normalizeACPAgentID(field.id)
    if (id === 'command') return t('bots.settings.acpCommand')
    if (id === 'arguments') return t('bots.settings.acpArguments')
  }
  return field.label || field.id || ''
}

export function acpManagedFieldHelp(
  profile: AcpprofilePublicProfile,
  field: AcpprofileManagedField,
  t: AcpSetupModeLabelTranslate,
): string {
  if (isACPAgent(profile.id)) {
    const id = normalizeACPAgentID(field.id)
    if (id === 'command') return t('bots.settings.acpCommandHelp')
    if (id === 'arguments') return t('bots.settings.acpArgumentsHelp')
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
  return `acp-${normalizeACPAgentID(profile.id) || 'agent'}-${normalizeACPAgentID(field.id) || 'field'}`
}

export function acpManagedFieldAutocomplete(field: AcpprofileManagedField): string {
  return field.type === 'password' ? 'new-password' : 'off'
}

/** Fields shown on create surfaces (new bot + onboarding) in api_key mode only. */
export function filterCreateVisibleManagedFields(
  profile: AcpprofilePublicProfile,
  _managed: Record<string, string>,
  setupMode: string,
): AcpprofileManagedField[] {
  if (setupMode !== 'api_key') return []
  return (profile.managed_fields ?? []).filter((field) => {
    const id = normalizeACPAgentID(field.id)
    return !(!id || id === 'provider_id' || id === 'oauth_token')
  })
}

/** Fields shown in bot settings for the active setup mode. */
export function filterSettingsVisibleManagedFields(
  profile: AcpprofilePublicProfile,
  _managed: Record<string, string>,
  _setupMode: string,
): AcpprofileManagedField[] {
  return (profile.managed_fields ?? []).filter((field) => {
    const id = normalizeACPAgentID(field.id)
    return id !== 'provider_id'
  })
}
