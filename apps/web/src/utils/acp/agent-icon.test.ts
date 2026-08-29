import { describe, expect, it } from 'vitest'
import { Bot as BotIcon } from 'lucide-vue-next'
import { Acp, CodexColor, HermesAgent } from '@memohai/icon'
import { acpAgentDisplayName, acpAgentIcon } from './agent-icon'

describe('acp-agent-icon', () => {
  it('maps known ACP agents to their brand icons', () => {
    expect(acpAgentIcon('acp', true)).toBe(Acp)
    expect(acpAgentIcon('codex', true)).toBe(CodexColor)
    expect(acpAgentIcon('hermes', true)).toBe(HermesAgent)
    expect(acpAgentIcon('hermes')).toBe(HermesAgent)
  })

  it('falls back to a generic bot icon for unknown agents', () => {
    expect(acpAgentIcon('custom-agent')).toBe(BotIcon)
  })

  it('normalizes Hermes display name', () => {
    expect(acpAgentDisplayName('hermes')).toBe('Hermes')
    expect(acpAgentDisplayName('ACP')).toBe('ACP')
  })
})
