import { expect, it, vi } from 'vitest'
import { createRuntimeIntegration, wsFrameErrorCode, type RuntimeIntegrationDeps } from './runtime-integration'
import { failureStage } from './send'
import { createEmptyRuntimeProjection } from './runtime-projection'
import type { RuntimeCurrentRunView } from '@/composables/api/useChat'

it('模式保存的所有帧均不刷新历史或改写上轮回复', () => {
  const applyRuntimeTranscript = vi.fn()
  const refreshCurrentSession = vi.fn()
  const resyncRuntimeTranscript = vi.fn()
  const deps = {
    bumpProjectionVersion: vi.fn(),
    chatViews: { getSession: () => ({ transcript: { applyRuntimeTranscript } }) },
    decisions: { observeRun: vi.fn() },
    refreshCurrentSession,
    resyncRuntimeTranscript,
  } as unknown as RuntimeIntegrationDeps
  const integration = createRuntimeIntegration(deps)
  const event = {
    type: 'runtime_snapshot' as const, session_id: 'session', epoch: 'epoch', seq: 1,
    snapshot: { bot_id: 'bot', session_id: 'session', epoch: 'epoch', seq: 1, updated_at: '' },
  }
  let previous = createEmptyRuntimeProjection()
  for (const status of ['admitting', 'running', 'finishing', 'completed'] as const) {
    const run: RuntimeCurrentRunView = {
      run_id: 'config', turn_id: 'config-turn', generation: '1', status,
      configuration_only: true, messages: [], started_at: '', updated_at: '',
    }
    const current = { ...createEmptyRuntimeProjection(), currentRunView: run }
    integration.handleProjection('bot', 'session', { previous, current, event })
    previous = current
  }
  integration.handleProjection('bot', 'session', { previous, current: createEmptyRuntimeProjection(), event })
  expect(applyRuntimeTranscript).not.toHaveBeenCalled()
  expect(refreshCurrentSession).not.toHaveBeenCalled()
  expect(resyncRuntimeTranscript).not.toHaveBeenCalled()
  expect(deps.bumpProjectionVersion).toHaveBeenCalledTimes(5)
})

it('does not treat a replacement error code as replacement output', () => {
  const assistant = {
    id: 'assistant', role: 'assistant' as const, messages: [], timestamp: '', streaming: false,
  }
  expect(failureStage(assistant, true, false)).toBe('startup')
  expect(failureStage(assistant, true, true)).toBe('stream')
})

it('reads a WS failure code from the top level of the frame', () => {
  expect(wsFrameErrorCode({ code: ' agent.response_timeout ' })).toBe('agent.response_timeout')
  expect(wsFrameErrorCode({ code: '' })).toBe('')
  expect(wsFrameErrorCode({})).toBe('')
})
