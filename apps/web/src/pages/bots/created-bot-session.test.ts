// @vitest-environment jsdom
import { beforeEach, expect, it } from 'vitest'
import { readCreatedBotSession, writeCreatedBotSession } from './created-bot-session'
beforeEach(() => sessionStorage.clear())
it('persists the exact created target across a reload without storing credentials', () => {
  const target = { botId: 'bot-1', botName: 'cat', displayName: 'Cat', agentId: 'agent-1', runtime: 'codex' as const, setupError: null }
  writeCreatedBotSession(target)
  expect(readCreatedBotSession()).toEqual(target)
  writeCreatedBotSession(null)
  expect(readCreatedBotSession()).toBeNull()
})
it('persists a plain Memoh Bot without any Agent fields', () => {
  const target = { botId: 'bot-2', botName: 'mochi', displayName: 'Mochi', avatarUrl: 'https://a/b.png', setupError: 'workspace setup failed', settings: { chat_model_id: 'm1' } }
  writeCreatedBotSession(target, true)
  expect(readCreatedBotSession(true)).toEqual({ ...target, settings: { chat_model_id: 'm1', memory_provider_id: undefined, reasoning_effort: undefined } })
  expect(readCreatedBotSession()).toBeNull()
})
it.each(['{', '{}', '{"botId":"bot","agentId":"agent","runtime":"acp"}', '{"botId":"bot","agentId":1}'])('rejects invalid saved targets: %s', (raw) => {
  sessionStorage.setItem('memoh:new-bot:authorization', raw)
  expect(readCreatedBotSession()).toBeNull()
})
