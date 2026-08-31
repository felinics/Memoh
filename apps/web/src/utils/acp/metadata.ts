import type { AcpprofileManagedField, AcpprofilePublicProfile } from '@memohai/sdk'

export const ACP_NO_PROJECT_MODE = 'none'
export const ACP_DEFAULT_PROJECT_MODE = 'project'
export const ACP_DEFAULT_PROJECT_PATH = '/data'
export const ACP_NO_PROJECT_ROOT = '/data/.memoh/acp-work/no-project'

export interface ACPAgentForm {
  enabled: boolean
  setup_mode: string
  managed: Record<string, string>
}

export interface ACPForm {
  agents: Record<string, ACPAgentForm>
}

export interface ACPAgentConfig {
  setupMode: string
  setupModeSet: boolean
  managed: Record<string, unknown>
}


export function readACPConfig(metadata: Record<string, unknown> | undefined, profiles: AcpprofilePublicProfile[]): ACPForm {
  const out: ACPForm = { agents: {} }
  const acp = isRecord(metadata?.acp) ? metadata.acp : {}
  const agents = isRecord(acp.agents) ? acp.agents : {}
  for (const profile of profiles) {
    const id = normalizeACPAgentID(profile.id)
    if (!id) continue
    const defaults = emptyACPAgentForm(profile)
    const raw = agents[id]
    if (typeof raw === 'boolean') {
      out.agents[id] = { ...defaults, enabled: raw }
      continue
    }
    const record = isRecord(raw) ? raw : {}
    const managed = isRecord(record.managed) ? record.managed : {}
    out.agents[id] = {
      enabled: typeof record.enabled === 'boolean' ? record.enabled : legacyEnabled(acp, id),
      setup_mode: normalizeSetupMode(typeof record.setup_mode === 'string' ? record.setup_mode : defaults.setup_mode, managed),
      managed: fieldsFromProfile(profile, managed),
    }
  }
  return out
}

export function normalizeACPForm(source: ACPForm, profiles: AcpprofilePublicProfile[]): ACPForm {
  const out: ACPForm = { agents: {} }
  for (const profile of profiles) {
    const id = normalizeACPAgentID(profile.id)
    if (!id) continue
    const agent = source.agents[id] ?? emptyACPAgentForm(profile)
    out.agents[id] = {
      enabled: !!agent.enabled,
      setup_mode: normalizeSetupMode(agent.setup_mode || defaultSetupMode(profile), agent.managed),
      managed: fieldsFromProfile(profile, agent.managed),
    }
  }
  return out
}

export function withACPMetadata(metadata: Record<string, unknown> | undefined, acpForm: ACPForm, profiles: AcpprofilePublicProfile[] = []): Record<string, unknown> {
  const nextMetadata = isRecord(metadata) ? { ...metadata } : {}
  const currentACP = isRecord(nextMetadata.acp) ? nextMetadata.acp : {}
  const acp: Record<string, unknown> = { ...currentACP }
  const currentAgents = isRecord(acp.agents) ? acp.agents : {}
  acp.agents = {
    ...currentAgents,
    ...serializeACPAgents(metadata, acpForm, profiles),
  }
  delete acp.codex_enabled
  delete acp.enabled_agents
  nextMetadata.acp = acp
  return nextMetadata
}

// Agent creation must only touch the selected profile. Re-serializing every
// profile would turn defaults or stale client state for an unrelated Agent
// into an explicit server update and can make that unrelated config fail
// validation before the new BotAgent row is created.
export function withEnabledACPAgentMetadataIfConfigured(
  metadata: Record<string, unknown> | undefined,
  profile: AcpprofilePublicProfile,
): Record<string, unknown> | undefined {
  const form = readACPConfig(metadata, [profile])
  const agent = ensureACPAgentForm(form, profile)
  if (findMissingRequiredManagedField(profile, agent.managed, agent.setup_mode)) return undefined
  agent.enabled = true
  return withACPMetadata(metadata, form, [profile])
}

export function findMissingRequiredManagedField(profile: AcpprofilePublicProfile | null | undefined, managed: Record<string, unknown>, setupMode: string): AcpprofileManagedField | null {
  const mode = normalizeSetupMode(setupMode, managed)
  if (!profile) return null
  if (!profileSupportsSetupMode(profile, mode)) {
    return { id: 'setup_mode', label: 'Setup', type: 'text', required: true }
  }
  if (mode === 'self') return null
  for (const field of profile.managed_fields ?? []) {
    const id = normalizeACPAgentID(field.id)
    if (!id || !field.required) continue
    if (!String(managed[id] ?? '').trim()) return field
  }
  return null
}

function profileSupportsSetupMode(profile: AcpprofilePublicProfile, mode: string): boolean {
  const modes = profile.setup_modes?.filter(Boolean)
  if (!modes || modes.length === 0) return true
  return modes.some(supported => normalizeACPAgentID(supported) === mode)
}

export function readACPAgentConfig(metadata: Record<string, unknown> | undefined, rawAgentID: string | undefined): ACPAgentConfig {
  const agentID = normalizeACPAgentID(rawAgentID)
  const acp = isRecord(metadata?.acp) ? metadata.acp : {}
  const agents = isRecord(acp.agents) ? acp.agents : {}
  const raw = agentID ? agents[agentID] : undefined
  const record = isRecord(raw) ? raw : {}
  const managed = isRecord(record.managed) ? record.managed : {}
  return {
    setupMode: normalizeSetupMode(typeof record.setup_mode === 'string' ? record.setup_mode : '', managed),
    setupModeSet: typeof record.setup_mode === 'string' && record.setup_mode.trim() !== '',
    managed,
  }
}

export function isACPAgentEnabled(metadata: Record<string, unknown> | undefined, rawAgentID: unknown): boolean {
  const agentID = normalizeACPAgentID(rawAgentID)
  if (!agentID || !metadata) return false
  const acp = isRecord(metadata.acp) ? metadata.acp : {}
  const agents = isRecord(acp.agents) ? acp.agents : {}
  const raw = agents[agentID]
  if (typeof raw === 'boolean') return raw
  if (isRecord(raw) && typeof raw.enabled === 'boolean') return raw.enabled
  return legacyEnabled(acp, agentID)
}

export function createACPNoProjectPath(): string {
  return `${ACP_NO_PROJECT_ROOT}/${randomID()}`
}

export function emptyACPAgentForm(profile: AcpprofilePublicProfile): ACPAgentForm {
  return {
    enabled: false,
    setup_mode: defaultSetupMode(profile),
    managed: fieldsFromProfile(profile, {}),
  }
}

export function ensureACPAgentForm(form: ACPForm, profile: AcpprofilePublicProfile): ACPAgentForm {
  const id = normalizeACPAgentID(profile.id)
  if (!id) return emptyACPAgentForm(profile)
  if (!form.agents[id]) {
    form.agents[id] = emptyACPAgentForm(profile)
  }
  return form.agents[id]
}

export function fieldsFromProfile(profile: AcpprofilePublicProfile, source: Record<string, unknown>): Record<string, string> {
  const values: Record<string, string> = {}
  for (const field of profile.managed_fields ?? []) {
    const id = normalizeACPAgentID(field.id)
    if (!id) continue
    const value = source[id]
    values[id] = typeof value === 'string' ? value : ''
  }
  return values
}

// 首项即默认:setup_modes 的顺序由后端 profile 定义,它既是分段控件的显示顺序,
// 也是默认选中项 —— 一处真相。前端不再另立「有 api_key 就选 api_key」的偏好,
// 那条规则会让后端把某个模式提到首位的意图只兑现一半(排序变了、默认没变)。
export function defaultSetupMode(profile: AcpprofilePublicProfile): string {
  const modes = (profile.setup_modes ?? []).filter(Boolean)
  return normalizeSetupMode(modes[0] ?? 'api_key')
}

export function normalizeACPAgentID(value: unknown): string {
  return typeof value === 'string' ? value.trim().toLowerCase() : ''
}

function legacyEnabled(acp: Record<string, unknown>, id: string): boolean {
  if (Array.isArray(acp.enabled_agents) && acp.enabled_agents.some((item) => normalizeACPAgentID(item) === id)) return true
  if (id === 'codex' && typeof acp.codex_enabled === 'boolean') return acp.codex_enabled
  return false
}

function normalizeSetupMode(mode: string, managed: Record<string, unknown> = {}): string {
  const value = normalizeACPAgentID(mode)
  if (value === 'oauth' || value === 'self') return value
  if (value === 'managed') {
    const legacyAuthType = normalizeACPAgentID(managed.auth_type)
    return legacyAuthType === 'provider_oauth' || legacyAuthType === 'oauth' ? 'oauth' : 'api_key'
  }
  if (value === 'api_key') return value
  return value || 'api_key'
}

function serializeACPAgents(metadata: Record<string, unknown> | undefined, acpForm: ACPForm, profiles: AcpprofilePublicProfile[]): Record<string, unknown> {
  const profileByID = new Map(profiles.map(profile => [normalizeACPAgentID(profile.id), profile]))
  const out: Record<string, unknown> = {}
  for (const [rawAgentID, agent] of Object.entries(acpForm.agents)) {
    const agentID = normalizeACPAgentID(rawAgentID)
    const profile = profileByID.get(agentID)
    const managed: Record<string, unknown> = { ...agent.managed }
    if (profile) {
      const existingManaged = existingManagedFields(metadata, agentID)
      for (const field of profile.managed_fields ?? []) {
        const fieldID = normalizeACPAgentID(field.id)
        if (!fieldID || !isSensitiveManagedField(field)) continue
        const value = managed[fieldID]
        const existing = existingManaged[fieldID]
        if (typeof value === 'string' && value.trim() === '' && typeof existing === 'string' && existing.trim() !== '') {
          managed[fieldID] = null
        }
      }
    }
    out[agentID || rawAgentID] = {
      enabled: !!agent.enabled,
      setup_mode: normalizeSetupMode(agent.setup_mode, managed),
      managed,
    }
  }
  return out
}

function existingManagedFields(metadata: Record<string, unknown> | undefined, agentID: string): Record<string, unknown> {
  const acp = isRecord(metadata?.acp) ? metadata.acp : {}
  const agents = isRecord(acp.agents) ? acp.agents : {}
  const agent = isRecord(agents[agentID]) ? agents[agentID] : {}
  return isRecord(agent.managed) ? agent.managed : {}
}

function isSensitiveManagedField(field: AcpprofileManagedField): boolean {
  return field.sensitive === true || field.type === 'password'
}

function randomID(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}
