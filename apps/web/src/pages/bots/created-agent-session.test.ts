// @vitest-environment jsdom
import { beforeEach, expect, it } from 'vitest'
import { readCreatedAgentSession, writeCreatedAgentSession } from './created-agent-session'
beforeEach(() => sessionStorage.clear())
it('persists the exact created target across a reload without storing credentials', () => {
  const target = { botId: 'bot-1', botName: 'cat', displayName: 'Cat', agentId: 'agent-1', runtime: 'codex' as const, setupError: null }
  writeCreatedAgentSession(target)
  expect(readCreatedAgentSession()).toEqual(target)
  writeCreatedAgentSession(null)
  expect(readCreatedAgentSession()).toBeNull()
})
it.each(['{', '{}', '{"botId":"bot","agentId":"agent","runtime":"acp"}'])('rejects invalid saved targets: %s', (raw) => {
  sessionStorage.setItem('memoh:new-bot:authorization', raw)
  expect(readCreatedAgentSession()).toBeNull()
})
