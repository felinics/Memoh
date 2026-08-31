import type { Component } from 'vue'
import { Bot as BotIcon } from 'lucide-vue-next'
import { Acp, ClaudeCode, ClaudeCodeColor, Codex, CodexColor } from '@memohai/icon'
import { normalizeACPAgentID } from './metadata'

export function acpAgentIcon(agentID: unknown, color = false): Component {
  if (isACPAgent(agentID)) return Acp
  if (isCodexAgent(agentID)) return color ? CodexColor : Codex
  if (isClaudeCodeAgent(agentID)) return color ? ClaudeCodeColor : ClaudeCode
  return BotIcon
}

export function isACPAgent(agentID: unknown): boolean {
  return normalizeACPAgentID(agentID) === 'acp'
}

function isCodexAgent(agentID: unknown): boolean {
  return normalizeACPAgentID(agentID) === 'codex'
}

function isClaudeCodeAgent(agentID: unknown): boolean {
  return normalizeACPAgentID(agentID) === 'claude-code'
}

export function acpAgentDisplayName(agentID: unknown, fallback = ''): string {
  const normalized = normalizeACPAgentID(agentID)
  if (!normalized) return fallback
  if (isACPAgent(normalized)) return 'ACP'
  if (isCodexAgent(normalized)) return 'Codex'
  if (isClaudeCodeAgent(normalized)) return 'Claude Code'
  return typeof agentID === 'string' ? agentID.trim() : normalized
}
