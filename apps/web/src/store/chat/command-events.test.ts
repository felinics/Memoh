import { ref } from 'vue'
import { expect, it } from 'vitest'
import { createCommandEventRegistry } from './command-events'

it('完成事件归属原会话，过期或已关闭的命令不能覆盖面板', () => {
  const sessionId = ref<string | null>('a')
  const registry = createCommandEventRegistry({ currentBotId: ref('bot'), sessionId })
  const scope = registry.currentCommandScope()
  const completeA = registry.beginCommandEvent({ type: 'command_result', terminal: false, action_id: 'mcp' }, scope)
  const completeB = registry.beginCommandEvent({ type: 'command_result', terminal: false, action_id: 'status' }, scope)
  completeA({ type: 'command_error', terminal: true, error: { code: 'late', message: 'late' } })
  expect(registry.commandEventForScope(scope)?.action_id).toBe('status')

  sessionId.value = 'b'
  completeB({ type: 'command_result', terminal: true, action_id: 'status' })
  expect(registry.commandEventForScope(scope)?.terminal).toBe(true)
  expect(registry.commandEvent.value).toBeNull()
  registry.clearCommandEvent(scope)
  completeB({ type: 'command_result', terminal: true })
  expect(registry.commandEventForScope(scope)).toBeNull()
})
