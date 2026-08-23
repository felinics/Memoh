import type { AcpprofileManagedField, AcpprofilePublicProfile } from '@memohai/sdk'
import { isACPAgent, isClaudeCodeAgent, isCodexAgent } from './agent-icon'
import { hermesAPIKeyPlaceholder, isHermesCustomProvider, hermesProviderValue } from './hermes'
import { normalizeACPAgentID } from './metadata'

export type AcpSetupModeLabelTranslate = (key: string) => string

export function acpSetupModes(profile: AcpprofilePublicProfile): string[] {
  const modes = (profile.setup_modes ?? []).filter(Boolean)
  return modes.length > 0 ? modes : ['api_key']
}

export function acpSetupModeLabel(
  profile: AcpprofilePublicProfile,
  mode: string,
  t: AcpSetupModeLabelTranslate,
): string {
  if (mode === 'api_key') return t('bots.settings.acpSetupApiKey')
  if (mode === 'oauth') {
    if (isCodexAgent(profile.id)) return t('bots.settings.acpSetupChatGPT')
    if (isClaudeCodeAgent(profile.id)) return t('bots.settings.acpSetupClaude')
    return t('bots.settings.acpSetupOAuth')
  }
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
  const isHermes = normalizeACPAgentID(profile.id) === 'hermes'
  const id = normalizeACPAgentID(field.id)
  if (isHermes && (id === 'provider' || id === 'model')) return ''
  return field.help || ''
}

export function acpManagedPlaceholder(
  profile: AcpprofilePublicProfile,
  field: AcpprofileManagedField,
  hermesProvider: string,
): string | undefined {
  const isHermes = normalizeACPAgentID(profile.id) === 'hermes'
  if (isHermes && normalizeACPAgentID(field.id) === 'api_key') {
    return hermesAPIKeyPlaceholder(hermesProvider, field.placeholder)
  }
  return field.placeholder
}

export function acpManagedFieldName(profile: AcpprofilePublicProfile, field: AcpprofileManagedField): string {
  return `acp-${normalizeACPAgentID(profile.id) || 'agent'}-${normalizeACPAgentID(field.id) || 'field'}`
}

export function acpManagedFieldAutocomplete(field: AcpprofileManagedField): string {
  return field.type === 'password' ? 'new-password' : 'off'
}

function isHermesProfile(profile: AcpprofilePublicProfile): boolean {
  return normalizeACPAgentID(profile.id) === 'hermes'
}

/** Fields shown on create surfaces (new bot + onboarding) in api_key mode only. */
export function filterCreateVisibleManagedFields(
  profile: AcpprofilePublicProfile,
  managed: Record<string, string>,
  setupMode: string,
): AcpprofileManagedField[] {
  if (setupMode !== 'api_key') return []
  const isHermes = isHermesProfile(profile)
  const hermesProvider = hermesProviderValue(managed.provider)
  return (profile.managed_fields ?? []).filter((field) => {
    const id = normalizeACPAgentID(field.id)
    if (!id || id === 'provider_id' || id === 'oauth_token') return false
    if (isHermes && id === 'base_url') return isHermesCustomProvider(hermesProvider)
    return true
  })
}

/** Fields shown in bot settings for the active setup mode (includes OAuth-aware filtering). */
export function filterSettingsVisibleManagedFields(
  profile: AcpprofilePublicProfile,
  managed: Record<string, string>,
  setupMode: string,
): AcpprofileManagedField[] {
  const isHermes = isHermesProfile(profile)
  const isCodex = isCodexAgent(profile.id)
  const isClaude = isClaudeCodeAgent(profile.id)
  const hermesProvider = hermesProviderValue(managed.provider)
  return (profile.managed_fields ?? []).filter((field) => {
    const id = normalizeACPAgentID(field.id)
    if (id === 'provider_id') return false
    if (isHermes && id === 'base_url') return isHermesCustomProvider(hermesProvider)
    if (isCodex && setupMode === 'oauth') return false
    if (isClaude) {
      if (id === 'api_key') return setupMode === 'api_key'
      if (id === 'oauth_token') return false
    }
    return true
  })
}
