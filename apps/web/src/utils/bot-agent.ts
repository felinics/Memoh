import type { Component } from 'vue'
import type { AcpprofilePublicProfile, BotagentsBotAgent } from '@memohai/sdk'
import { acpAgentDisplayName, acpAgentIcon, normalizeACPAgentID } from '@/utils/acp'

export const BOT_AGENT_RUNTIME_ACP = 'acp'
export const BOT_AGENT_RUNTIME_CODEX = 'codex'
export const BOT_AGENT_RUNTIME_CLAUDE_CODE = 'claude-code'

export type BotAgentRuntime =
  | typeof BOT_AGENT_RUNTIME_ACP
  | typeof BOT_AGENT_RUNTIME_CODEX
  | typeof BOT_AGENT_RUNTIME_CLAUDE_CODE

export interface BotAgentRuntimeOption {
  value: string
  runtime: BotAgentRuntime
  label: string
  description?: string
  keywords: string[]
}

const directRuntimeProviders = [
  BOT_AGENT_RUNTIME_CODEX,
  BOT_AGENT_RUNTIME_CLAUDE_CODE,
] as const

function objectValue(value: unknown): Record<string, unknown> | undefined {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined
  return value as Record<string, unknown>
}

export function normalizeBotAgentRuntime(value: unknown): BotAgentRuntime | '' {
  const runtime = normalizeACPAgentID(value)
  switch (runtime) {
    case BOT_AGENT_RUNTIME_ACP:
    case BOT_AGENT_RUNTIME_CODEX:
    case BOT_AGENT_RUNTIME_CLAUDE_CODE:
      return runtime
    default:
      return ''
  }
}

export function botAgentRuntimeForProvider(provider: unknown): BotAgentRuntime {
  const normalized = normalizeACPAgentID(provider)
  if (normalized === BOT_AGENT_RUNTIME_CODEX) return BOT_AGENT_RUNTIME_CODEX
  if (normalized === BOT_AGENT_RUNTIME_CLAUDE_CODE) return BOT_AGENT_RUNTIME_CLAUDE_CODE
  return BOT_AGENT_RUNTIME_ACP
}

export function botAgentRuntimeOptions(profiles: AcpprofilePublicProfile[]): BotAgentRuntimeOption[] {
  const direct = directRuntimeProviders.map(provider => ({
    value: provider,
    runtime: provider,
    label: acpAgentDisplayName(provider, provider),
    keywords: [provider, acpAgentDisplayName(provider, provider)],
  }))
  const acp = profiles.flatMap((profile) => {
    const provider = normalizeACPAgentID(profile.id)
    if (!provider || botAgentRuntimeForProvider(provider) !== BOT_AGENT_RUNTIME_ACP) return []
    const label = profile.display_name?.trim() || acpAgentDisplayName(provider, provider)
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
  if (runtime !== BOT_AGENT_RUNTIME_CODEX && runtime !== BOT_AGENT_RUNTIME_CLAUDE_CODE) return undefined
  return {
    provider: runtime,
    auth: runtime === BOT_AGENT_RUNTIME_CODEX ? 'chatgpt' : 'workspace',
  }
}

export function isDirectBotAgentConfigured(
  agent: Pick<BotagentsBotAgent, 'runtime' | 'metadata' | 'agent_credential_id'> | null | undefined,
): boolean | null {
  const runtime = normalizeBotAgentRuntime(agent?.runtime)
  const config = objectValue(agent?.metadata)
  const auth = normalizeACPAgentID(config?.auth)
  if (runtime === BOT_AGENT_RUNTIME_CODEX) {
    return (auth === 'chatgpt' || auth === 'api_key') && !!agent?.agent_credential_id
  }
  if (runtime === BOT_AGENT_RUNTIME_CLAUDE_CODE) {
    if (auth === 'workspace') return true
    if (auth === 'api_key' || auth === 'oauth_token') return !!agent?.agent_credential_id
    return false
  }
  return null
}

export function botAgentProvider(agent: Pick<BotagentsBotAgent, 'runtime' | 'metadata'> | null | undefined): string {
  const provider = normalizeACPAgentID(agent?.metadata?.provider)
  if (provider) return provider
  const runtime = normalizeBotAgentRuntime(agent?.runtime)
  return runtime === BOT_AGENT_RUNTIME_CODEX || runtime === BOT_AGENT_RUNTIME_CLAUDE_CODE ? runtime : ''
}

export function botAgentIcon(agent: Pick<BotagentsBotAgent, 'runtime' | 'metadata'> | null | undefined, color = false): Component {
  return acpAgentIcon(botAgentProvider(agent), color)
}

export function sessionAgentProvider(
  runtimeType: unknown,
  runtimeMetadata: Record<string, unknown> | undefined,
  metadata: Record<string, unknown> | undefined,
): string {
  const runtime = normalizeBotAgentRuntime(runtimeType)
  if (runtime === BOT_AGENT_RUNTIME_CODEX || runtime === BOT_AGENT_RUNTIME_CLAUDE_CODE) return runtime
  return normalizeACPAgentID(runtimeMetadata?.acp_agent_id ?? metadata?.acp_agent_id)
}

export function botAgentName(agent: Pick<BotagentsBotAgent, 'name' | 'runtime' | 'metadata'> | null | undefined): string {
  const name = agent?.name?.trim()
  if (name) return name
  const provider = botAgentProvider(agent)
  return acpAgentDisplayName(provider, provider)
}

export function suggestBotAgentName(
  provider: string,
  agents: Array<Pick<BotagentsBotAgent, 'name'>>,
  fallback = '',
): string {
  const base = fallback.trim() || acpAgentDisplayName(provider, provider) || provider
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
