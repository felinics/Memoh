import { externalAgentDisplayName, externalAgentIcon, normalizeAgentID } from '@/utils/external-agent'
import type { Component } from 'vue'
import type { AcpprofilePublicProfile, BotagentsBotAgent } from '@memohai/sdk'

export const BOT_AGENT_RUNTIME_ACP = 'acp'
export const BOT_AGENT_RUNTIME_CODEX = 'codex'
export const BOT_AGENT_RUNTIME_CLAUDE_CODE = 'claude-code'

// The built-in External Agents that run without an ACP profile. Every place
// that treats "a direct runtime" alike reads this list; the string doubles as
// the session runtime type and the Bot Agent provider.
export const DIRECT_BOT_AGENT_RUNTIMES = [
  BOT_AGENT_RUNTIME_CODEX,
  BOT_AGENT_RUNTIME_CLAUDE_CODE,
] as const

export type DirectBotAgentRuntime = typeof DIRECT_BOT_AGENT_RUNTIMES[number]

export type BotAgentRuntime =
  | typeof BOT_AGENT_RUNTIME_ACP
  | DirectBotAgentRuntime

export interface BotAgentRuntimeOption {
  value: string
  runtime: BotAgentRuntime
  label: string
  description?: string
  keywords: string[]
}

export type AgentAuthMode = 'chatgpt' | 'workspace' | 'oauth_token' | 'api_key'
export type DeviceLoginAuthMode = Extract<AgentAuthMode, 'chatgpt'>

// Each runtime's auth modes as an existing Agent offers them. The first is the
// one a new Agent starts in.
export const DIRECT_BOT_AGENT_AUTH_MODES: Record<DirectBotAgentRuntime, readonly [AgentAuthMode, ...AgentAuthMode[]]> = {
  [BOT_AGENT_RUNTIME_CODEX]: ['chatgpt', 'api_key'],
  [BOT_AGENT_RUNTIME_CLAUDE_CODE]: ['workspace', 'api_key', 'oauth_token'],
}

// i18n keys of an auth mode: its chooser option and what choosing it means.
export const AGENT_AUTH_MODE_COPY: Record<AgentAuthMode, { label: string, description: string }> = {
  chatgpt: { label: 'bots.agent.authChatGPT', description: 'bots.agent.authChatGPTDescription' },
  workspace: { label: 'bots.agent.authWorkspace', description: 'bots.agent.authWorkspaceDescription' },
  oauth_token: { label: 'bots.agent.authOAuthToken', description: 'bots.agent.authOAuthTokenDescription' },
  api_key: { label: 'bots.agent.apiKey', description: 'bots.agent.authApiKeyDescription' },
}

export function isAgentAuthMode(value: string): value is AgentAuthMode {
  return Object.hasOwn(AGENT_AUTH_MODE_COPY, value)
}

export const DIRECT_BOT_AGENT_API_KEY_AUTH_KIND: Record<DirectBotAgentRuntime, string> = {
  [BOT_AGENT_RUNTIME_CODEX]: 'openai_api_key',
  [BOT_AGENT_RUNTIME_CLAUDE_CODE]: 'anthropic_api_key',
}

// Account sign-ins completed with a device code, by auth mode: the credential
// kind each one produces and the i18n keys its sign-in shows. The server runs
// the sign-in before the Bot exists; an existing Agent signs in on its own
// runtime process. An existing Agent's settings already explain the auth
// mode, so its account row says where the account is kept instead
// (agentDescription).
export const DEVICE_LOGINS: Record<DeviceLoginAuthMode, {
  authKind: string
  agentDescription: string
  connectedDescription: string
  hint: string
  success: string
  failed: string
}> = {
  chatgpt: {
    authKind: 'openai_codex_oauth',
    agentDescription: 'bots.agent.chatgptAccountDescription',
    connectedDescription: 'bots.agent.chatgptAccountConnectedDescription',
    hint: 'bots.agent.codexDeviceHint',
    success: 'bots.agent.codexLoginSuccess',
    failed: 'bots.agent.codexLoginFailed',
  },
}

// The managed workspace dependency that provides each runtime's CLI.
export const DIRECT_BOT_AGENT_DEPENDENCY_ID: Record<DirectBotAgentRuntime, string> = {
  [BOT_AGENT_RUNTIME_CODEX]: 'codex',
  [BOT_AGENT_RUNTIME_CLAUDE_CODE]: 'claude-code',
}

export function isDeviceLoginAuthMode(value: string): value is DeviceLoginAuthMode {
  return Object.hasOwn(DEVICE_LOGINS, value)
}

function deviceLoginAuth(authKind: string): DeviceLoginAuthMode | undefined {
  return (Object.keys(DEVICE_LOGINS) as DeviceLoginAuthMode[]).find(auth => DEVICE_LOGINS[auth].authKind === authKind)
}

export function isDeviceLoginAuthKind(authKind: string): boolean {
  return deviceLoginAuth(authKind) !== undefined
}

// The auth mode an Agent needs for a credential of this kind to be accepted.
export function agentAuthForCredentialKind(authKind: string): AgentAuthMode {
  return deviceLoginAuth(authKind) ?? (authKind === 'claude_code_oauth' ? 'oauth_token' : 'api_key')
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined
  return value as Record<string, unknown>
}

export function isDirectBotAgentRuntime(value: unknown): value is DirectBotAgentRuntime {
  return (DIRECT_BOT_AGENT_RUNTIMES as readonly unknown[]).includes(value)
}

export function normalizeBotAgentRuntime(value: unknown): BotAgentRuntime | '' {
  const runtime = normalizeAgentID(value)
  return runtime === BOT_AGENT_RUNTIME_ACP || isDirectBotAgentRuntime(runtime) ? runtime : ''
}

export function botAgentRuntimeForProvider(provider: unknown): BotAgentRuntime {
  const normalized = normalizeAgentID(provider)
  return isDirectBotAgentRuntime(normalized) ? normalized : BOT_AGENT_RUNTIME_ACP
}

export function botAgentRuntimeOptions(profiles: AcpprofilePublicProfile[]): BotAgentRuntimeOption[] {
  const direct = DIRECT_BOT_AGENT_RUNTIMES.map(provider => ({
    value: provider,
    runtime: provider,
    label: externalAgentDisplayName(provider, provider),
    keywords: [provider, externalAgentDisplayName(provider, provider)],
  }))
  const acp = profiles.flatMap((profile) => {
    const provider = normalizeAgentID(profile.id)
    if (!provider || botAgentRuntimeForProvider(provider) !== BOT_AGENT_RUNTIME_ACP) return []
    const label = profile.display_name?.trim() || externalAgentDisplayName(provider, provider)
    return [{
      value: provider,
      runtime: BOT_AGENT_RUNTIME_ACP,
      label,
      description: profile.description?.trim() || undefined,
      keywords: [provider, label, profile.description ?? ''],
    } satisfies BotAgentRuntimeOption]
  })
  return [...direct, ...acp]
}

export function directBotAgentMetadata(runtime: BotAgentRuntime): Record<string, unknown> | undefined {
  if (!isDirectBotAgentRuntime(runtime)) return undefined
  return {
    provider: runtime,
    auth: DIRECT_BOT_AGENT_AUTH_MODES[runtime][0],
  }
}

export function isDirectBotAgentConfigured(
  agent: Pick<BotagentsBotAgent, 'runtime' | 'metadata' | 'agent_credential_id'> | null | undefined,
): boolean | null {
  const runtime = normalizeBotAgentRuntime(agent?.runtime)
  if (!isDirectBotAgentRuntime(runtime)) return null
  const auth = normalizeAgentID(objectValue(agent?.metadata)?.auth)
  if (!isAgentAuthMode(auth) || !DIRECT_BOT_AGENT_AUTH_MODES[runtime].includes(auth)) return false
  // The workspace's own login is the one mode that stores no credential.
  return auth === 'workspace' || !!agent?.agent_credential_id
}

export function botAgentProvider(agent: Pick<BotagentsBotAgent, 'runtime' | 'metadata'> | null | undefined): string {
  const provider = normalizeAgentID(agent?.metadata?.provider)
  if (provider) return provider
  const runtime = normalizeBotAgentRuntime(agent?.runtime)
  return isDirectBotAgentRuntime(runtime) ? runtime : ''
}

export function botAgentIcon(agent: Pick<BotagentsBotAgent, 'runtime' | 'metadata'> | null | undefined, color = false): Component {
  return externalAgentIcon(botAgentProvider(agent), color)
}

export function sessionAgentProvider(
  runtimeType: unknown,
  runtimeMetadata: Record<string, unknown> | undefined,
  metadata: Record<string, unknown> | undefined,
): string {
  const runtime = normalizeBotAgentRuntime(runtimeType)
  if (isDirectBotAgentRuntime(runtime)) return runtime
  return normalizeAgentID(runtimeMetadata?.acp_agent_id ?? metadata?.acp_agent_id)
}

export function botAgentName(agent: Pick<BotagentsBotAgent, 'name' | 'runtime' | 'metadata'> | null | undefined): string {
  const name = agent?.name?.trim()
  if (name) return name
  const provider = botAgentProvider(agent)
  return externalAgentDisplayName(provider, provider)
}

export function suggestBotAgentName(
  provider: string,
  agents: Array<Pick<BotagentsBotAgent, 'name'>>,
  fallback = '',
): string {
  const base = fallback.trim() || externalAgentDisplayName(provider, provider) || provider
  const names = new Set(
    agents
      .map(agent => agent.name?.trim().toLocaleLowerCase())
      .filter((name): name is string => !!name),
  )
  if (!names.has(base.toLocaleLowerCase())) return base
  let suffix = 2
  while (names.has(`${base} ${suffix}`.toLocaleLowerCase())) suffix += 1
  return `${base} ${suffix}`
}
