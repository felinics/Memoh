import { randomUUID } from '@/utils/uuid'
import { normalizeAgentID } from '@/utils/external-agent'
import type { AcpprofileManagedField, AcpprofilePublicProfile, BotagentsBotAgent } from '@memohai/sdk'

export const ACP_NO_PROJECT_MODE = 'none'
export const ACP_NO_PROJECT_ROOT = '/data/.memoh/acp-work/no-project'

export interface ACPAgentForm {
  setup_mode: string
  managed: Record<string, string>
}

type ACPAgentSource = Pick<BotagentsBotAgent, 'metadata'> | null | undefined

// 每个 ACP Agent 的启动配置存在它自己那一行的 metadata.managed 上。
// 所有自定义 ACP Agent 共用同一个 profile id(acp),所以配置不能按 profile 存取 ——
// 那样同一个 Bot 下的多个 Agent 会互相覆盖。
export function readACPAgentForm(agent: ACPAgentSource, profile: AcpprofilePublicProfile): ACPAgentForm {
  const managed = isRecord(agent?.metadata?.managed) ? agent.metadata.managed : {}
  return {
    setup_mode: defaultSetupMode(profile),
    managed: fieldsFromProfile(profile, managed),
  }
}

export function withACPAgentForm(agent: ACPAgentSource, form: ACPAgentForm): Record<string, unknown> {
  return {
    ...(isRecord(agent?.metadata) ? agent.metadata : {}),
    managed: { ...form.managed },
  }
}

export function isACPAgentConfigured(agent: ACPAgentSource, profile: AcpprofilePublicProfile | null | undefined): boolean {
  if (!profile) return false
  const form = readACPAgentForm(agent, profile)
  return findMissingRequiredManagedField(profile, form.managed, form.setup_mode) === null
}

export function findMissingRequiredManagedField(profile: AcpprofilePublicProfile | null | undefined, managed: Record<string, unknown>, setupMode: string): AcpprofileManagedField | null {
  const mode = normalizeSetupMode(setupMode, managed)
  if (!profile) return null
  if (!profileSupportsSetupMode(profile, mode)) {
    return { id: 'setup_mode', label: 'Setup', type: 'text', required: true }
  }
  if (mode === 'self') return null
  for (const field of profile.managed_fields ?? []) {
    const id = normalizeAgentID(field.id)
    if (!id || !field.required) continue
    if (!String(managed[id] ?? '').trim()) return field
  }
  return null
}

function profileSupportsSetupMode(profile: AcpprofilePublicProfile, mode: string): boolean {
  const modes = profile.setup_modes?.filter(Boolean)
  if (!modes || modes.length === 0) return true
  return modes.some(supported => normalizeAgentID(supported) === mode)
}

export function createACPNoProjectPath(): string {
  return `${ACP_NO_PROJECT_ROOT}/${randomUUID()}`
}

export function fieldsFromProfile(profile: AcpprofilePublicProfile, source: Record<string, unknown>): Record<string, string> {
  const values: Record<string, string> = {}
  for (const field of profile.managed_fields ?? []) {
    const id = normalizeAgentID(field.id)
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

function normalizeSetupMode(mode: string, managed: Record<string, unknown> = {}): string {
  const value = normalizeAgentID(mode)
  if (value === 'oauth' || value === 'self') return value
  if (value === 'managed') {
    const legacyAuthType = normalizeAgentID(managed.auth_type)
    return legacyAuthType === 'provider_oauth' || legacyAuthType === 'oauth' ? 'oauth' : 'api_key'
  }
  if (value === 'api_key') return value
  return value || 'api_key'
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}
