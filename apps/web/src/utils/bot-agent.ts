import type { Component } from 'vue'
import type { BotagentsBotAgent } from '@memohai/sdk'
import { acpAgentDisplayName, acpAgentIcon, normalizeACPAgentID } from '@/utils/acp'

export const BOT_AGENT_RUNTIME_ACP = 'acp'

export function botAgentProvider(agent: Pick<BotagentsBotAgent, 'metadata'> | null | undefined): string {
  return normalizeACPAgentID(agent?.metadata?.provider)
}

export function botAgentIcon(agent: Pick<BotagentsBotAgent, 'metadata'> | null | undefined, color = false): Component {
  return acpAgentIcon(botAgentProvider(agent), color)
}

export function botAgentName(agent: Pick<BotagentsBotAgent, 'name' | 'metadata'> | null | undefined): string {
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
