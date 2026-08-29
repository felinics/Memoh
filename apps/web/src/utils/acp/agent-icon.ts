import type { Component } from 'vue'
import { Bot as BotIcon } from 'lucide-vue-next'
import { Acp, ClaudeCode, ClaudeCodeColor, Codex, CodexColor, HermesAgent } from '@memohai/icon'
import { normalizeACPAgentID } from './metadata'

export function acpAgentIcon(agentID: unknown, color = false): Component {
  if (isACPAgent(agentID)) return Acp
  if (isCodexAgent(agentID)) return color ? CodexColor : Codex
  if (isClaudeCodeAgent(agentID)) return color ? ClaudeCodeColor : ClaudeCode
  if (isHermesAgent(agentID)) return HermesAgent
  return BotIcon
}

export function isACPAgent(agentID: unknown): boolean {
  return normalizeACPAgentID(agentID) === 'acp'
}

export function isCodexAgent(agentID: unknown): boolean {
  return normalizeACPAgentID(agentID) === 'codex'
}

export function isClaudeCodeAgent(agentID: unknown): boolean {
  return normalizeACPAgentID(agentID) === 'claude-code'
}

function isHermesAgent(agentID: unknown): boolean {
  return normalizeACPAgentID(agentID) === 'hermes'
}

export function acpAgentDisplayName(agentID: unknown, fallback = ''): string {
  const normalized = normalizeACPAgentID(agentID)
  if (!normalized) return fallback
  if (isACPAgent(normalized)) return 'ACP'
  if (isCodexAgent(normalized)) return 'Codex'
  if (isClaudeCodeAgent(normalized)) return 'Claude Code'
  if (isHermesAgent(normalized)) return 'Hermes'
  return typeof agentID === 'string' ? agentID.trim() : normalized
}
