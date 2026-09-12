import type { Component } from 'vue'
import { Bot as BotIcon } from 'lucide-vue-next'
import { Acp, ClaudeCode, ClaudeCodeColor, Codex, CodexColor } from '@memohai/icon'

export function externalAgentIcon(agentID: unknown, color = false): Component {
  if (isACPAgent(agentID)) return Acp
  if (isCodexAgent(agentID)) return color ? CodexColor : Codex
  if (isClaudeCodeAgent(agentID)) return color ? ClaudeCodeColor : ClaudeCode
  return BotIcon
}

export function isACPAgent(agentID: unknown): boolean {
  return normalizeAgentID(agentID) === 'acp'
}

function isCodexAgent(agentID: unknown): boolean {
  return normalizeAgentID(agentID) === 'codex'
}

function isClaudeCodeAgent(agentID: unknown): boolean {
  return normalizeAgentID(agentID) === 'claude-code'
}

export function externalAgentDisplayName(agentID: unknown, fallback = ''): string {
  const normalized = normalizeAgentID(agentID)
  if (!normalized) return fallback
  if (isACPAgent(normalized)) return 'ACP'
  if (isCodexAgent(normalized)) return 'Codex'
  if (isClaudeCodeAgent(normalized)) return 'Claude Code'
  return typeof agentID === 'string' ? agentID.trim() : normalized
}

export function normalizeAgentID(value: unknown): string {
  return typeof value === 'string' ? value.trim().toLowerCase() : ''
}

export const EXTERNAL_AGENT_DEFAULT_PROJECT_MODE = 'project'
export const EXTERNAL_AGENT_DEFAULT_PROJECT_PATH = '/data'
