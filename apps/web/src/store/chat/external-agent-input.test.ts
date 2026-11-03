import { describe, expect, it } from 'vitest'
import { normalizedExternalAgentInput, sameExternalAgentSessionInput } from './external-agent-staging'

describe('External Agent input normalization', () => {
  it('gives a default Direct Agent and an explicit selection the same identity', () => {
    const fromDefault = normalizedExternalAgentInput({ agentId: 'codex', botAgentId: 'agent-1' })
    expect(fromDefault.runtime).toBe('codex')
    expect(sameExternalAgentSessionInput(fromDefault, {
      agentId: 'codex', botAgentId: 'agent-1', runtime: 'codex',
    })).toBe(true)
  })

  it('keeps an explicit custom ACP runtime distinct from a Direct runtime', () => {
    const custom = { agentId: 'codex', runtime: 'acp' as const }
    expect(normalizedExternalAgentInput(custom).runtime).toBe('acp')
    expect(sameExternalAgentSessionInput(custom, { agentId: 'codex', runtime: 'codex' })).toBe(false)
  })

})
