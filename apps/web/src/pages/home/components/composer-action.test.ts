import { computed, reactive, shallowReactive } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import { composerActionPresentation, dispatchComposerAction, resolveComposerAction, type ComposerActionInput } from './composer-action'
import { SessionQueueSubmissionGate } from './session-queue-submission'

function input(overrides: Partial<ComposerActionInput> = {}): ComposerActionInput {
  return {
    text: '', hasFiles: false, hasSkills: false,
    streaming: false, awaitingFirstSend: false, stopping: false, queuePending: false,
    active: true, hasBot: true, hasSession: true, readOnly: false,
    loadingMessages: false, configPending: false, hasNoModel: false,
    modeUnavailable: false, modeChangeDisabled: false, agentUnavailable: false,
    localCommand: '', runtimeCommands: [], runtimeCommandsUnresolved: false,
    goalDraft: false, goalBlocked: false,
    ...overrides,
  }
}

function handlers() {
  return {
    stop: vi.fn(), send: vi.fn(), enqueue: vi.fn(),
    'local-command': vi.fn(), 'runtime-command': vi.fn(),
  }
}

describe('composer action', () => {
  it('switches stop → enqueue → stop when the running draft changes', () => {
    const state = reactive(input({ streaming: true }))
    const action = computed(() => resolveComposerAction(state))
    const button = computed(() => composerActionPresentation(action.value))
    expect(action.value.kind).toBe('stop')
    expect(button.value).toMatchObject({ icon: 'stop', labelKey: 'chat.stopResponse', disabled: false })
    state.text = 'another message'
    expect(action.value).toMatchObject({ kind: 'enqueue', mode: 'follow-up', text: 'another message' })
    expect(button.value).toMatchObject({ icon: 'send', labelKey: 'chat.queue.enqueueFollowUp', disabled: false })
    state.text = '  \n '
    expect(action.value.kind).toBe('stop')
    state.streaming = false
    expect(button.value).toMatchObject({ visible: false, disabled: true })
    state.text = 'next message'
    expect(action.value.kind).toBe('send')
  })

  it.each(['hasFiles', 'hasSkills'] as const)('keeps an unsupported queued %s draft as disabled send intent', async field => {
    const action = resolveComposerAction(input({ streaming: true, [field]: true }))
    expect(action).toMatchObject({ kind: 'enqueue', blockedReason: 'chat.queue.textOnly' })
    expect(composerActionPresentation(action)).toMatchObject({ icon: 'send', visible: true, disabled: true })
    const calls = handlers()
    await dispatchComposerAction(action, 'button', calls)
    await dispatchComposerAction(action, 'submit', calls)
    for (const call of Object.values(calls)) expect(call).not.toHaveBeenCalled()
  })

  it('uses the same dispatch for click and Enter, and never stops on empty Enter', async () => {
    const calls = handlers()
    const stop = resolveComposerAction(input({ streaming: true }))
    await dispatchComposerAction(stop, 'submit', calls)
    expect(calls.stop).not.toHaveBeenCalled()
    await dispatchComposerAction(stop, 'button', calls)
    expect(calls.stop).toHaveBeenCalledOnce()
    const enqueue = resolveComposerAction(input({ streaming: true, text: 'more' }))
    await dispatchComposerAction(enqueue, 'button', calls)
    await dispatchComposerAction(enqueue, 'submit', calls)
    expect(calls.enqueue).toHaveBeenNthCalledWith(1, enqueue)
    expect(calls.enqueue).toHaveBeenNthCalledWith(2, enqueue)
    expect(calls.send).not.toHaveBeenCalled()
  })

  it('locks both gestures until first-send acceptance, then follows the current draft', async () => {
    const state = input({ text: 'first message', awaitingFirstSend: true, streaming: true })
    const waiting = resolveComposerAction(state)
    expect(composerActionPresentation(waiting)).toMatchObject({ visible: true, disabled: true, busy: true, icon: 'busy' })
    const calls = handlers()
    await dispatchComposerAction(waiting, 'button', calls)
    await dispatchComposerAction(waiting, 'submit', calls)
    for (const call of Object.values(calls)) expect(call).not.toHaveBeenCalled()
    expect(resolveComposerAction({ ...state, text: '', awaitingFirstSend: false }).kind).toBe('stop')
    expect(resolveComposerAction({ ...state, awaitingFirstSend: false, streaming: false }).kind).toBe('send')
  })

  it.each(['succeed', 'fail'] as const)('blocks duplicate enqueues but allows stop during a request, then unlocks after %s', async outcome => {
    const state = reactive(input({ streaming: true, text: 'queued' }))
    const gate = shallowReactive(new SessionQueueSubmissionGate(() => '00000000-0000-4000-8000-000000000001'))
    const action = computed(() => resolveComposerAction({ ...state, queuePending: gate.pending }))
    const calls = handlers()
    let finish!: () => void
    calls.enqueue.mockImplementation(async () => {
      const submission = gate.begin({ botId: 'bot', sessionId: 'session', mode: 'follow-up', text: state.text })!
      state.text = ''
      await new Promise<void>(resolve => { finish = resolve })
      gate[outcome](submission)
      if (outcome === 'fail' && !state.text) state.text = 'queued'
    })
    const request = dispatchComposerAction(action.value, 'submit', calls)
    expect(action.value.kind).toBe('stop')
    await dispatchComposerAction(action.value, 'button', calls)
    expect(calls.stop).toHaveBeenCalledOnce()
    state.text = 'newer draft'
    expect(composerActionPresentation(action.value)).toMatchObject({ icon: 'send', disabled: true })
    await dispatchComposerAction(action.value, 'submit', calls)
    expect(calls.enqueue).toHaveBeenCalledOnce()
    finish()
    await request
    expect(action.value).toMatchObject({ kind: 'enqueue', text: 'newer draft' })
    expect(composerActionPresentation(action.value).disabled).toBe(false)
    state.streaming = false
    expect(action.value.kind).toBe('send')
  })

  it.each([
    { text: '/steer change direction', mode: 'steer' },
    { text: '/queue next task', mode: 'follow-up' },
  ] as const)('resolves $text explicitly even while idle', ({ text, mode }) => {
    expect(resolveComposerAction(input({ text }))).toEqual({ kind: 'enqueue', mode, text: text.split(' ').slice(1).join(' ') })
  })

  it.each([
    { text: '/queue', hasSession: true },
    { text: '/steer hello', hasSession: false },
    { text: 'follow-up', streaming: true, hasSession: false },
  ])('explains an invalid queue request: %j', overrides => {
    expect(resolveComposerAction(input(overrides))).toMatchObject({ kind: 'enqueue', blockedReason: 'errors.queue_request_invalid' })
  })

  it('preserves runtime command ownership of /queue and /steer', () => {
    for (const name of ['queue', 'steer']) {
      const command = { name, kind: 'read' as const }
      expect(resolveComposerAction(input({ streaming: true, text: `/${name} arg`, runtimeCommands: [command] })))
        .toEqual({ kind: 'runtime-command', command })
      expect(resolveComposerAction(input({ streaming: true, text: `/${name} arg`, runtimeCommands: [{ name, kind: 'turn' }] })))
        .toMatchObject({ kind: 'enqueue', blockedReason: 'errors.session_runtime.session_busy' })
      expect(resolveComposerAction(input({ text: `/${name} arg`, runtimeCommands: [{ name, kind: 'turn' }] })).kind).toBe('send')
    }
  })

  it('classifies local and runtime commands before generic queue input', async () => {
    const calls = handlers()
    const local = resolveComposerAction(input({ streaming: true, text: '/model', localCommand: 'model', hasNoModel: true }))
    await dispatchComposerAction(local, 'submit', calls)
    expect(calls['local-command']).toHaveBeenCalledWith(local)
    for (const id of ['goal', 'plan']) {
      expect(resolveComposerAction(input({ streaming: true, text: `/${id}`, localCommand: id, modeChangeDisabled: true })))
        .toMatchObject({ kind: 'local-command', blockedReason: 'errors.session_runtime.session_busy' })
    }
    const command = { name: 'compact', kind: 'operation' as const }
    expect(resolveComposerAction(input({ text: '/compact', streaming: true, runtimeCommands: [command] })))
      .toMatchObject({ kind: 'runtime-command', blockedReason: 'errors.session_runtime.session_busy' })
    const read = resolveComposerAction(input({ text: '/status', streaming: true, runtimeCommands: [{ name: 'status', kind: 'read' }] }))
    await dispatchComposerAction(read, 'button', calls)
    expect(calls['runtime-command']).toHaveBeenCalledWith(read)
  })

  it.each([
    { text: '/unknown', runtimeCommandsUnresolved: true },
    { text: 'my objective', goalDraft: true },
  ])('does not silently queue incompatible commands/goals: %j', overrides => {
    expect(resolveComposerAction(input({ streaming: true, ...overrides }))).toMatchObject({ kind: 'enqueue', blockedReason: 'errors.session_runtime.session_busy' })
  })

  it('applies model/configuration gates only to sending, and permissions to every action', () => {
    const gates = { hasNoModel: true, configPending: 'agent' as const, modeUnavailable: true, loadingMessages: true }
    expect(resolveComposerAction(input({ ...gates, streaming: true }))).toEqual({ kind: 'stop' })
    expect(resolveComposerAction(input({ hasNoModel: true, configPending: 'agent', streaming: true, text: 'more' }))).toEqual({ kind: 'enqueue', text: 'more', mode: 'follow-up' })
    expect(resolveComposerAction(input({ hasNoModel: true, text: 'hello' })).blockedReason).toBe('chat.composer.selectModel')
    expect(resolveComposerAction(input({ configPending: 'mode', text: 'hello' })).blockedReason).toBe('chat.composer.modeChanging')
    for (const text of ['', 'hello']) {
      expect(resolveComposerAction(input({ streaming: true, readOnly: true, text })).blockedReason).toBe('chat.readonlyHint')
    }
  })

  it('preserves goal and startup-agent validation', () => {
    expect(resolveComposerAction(input({ text: 'objective', goalDraft: true }))).toEqual({ kind: 'send', text: 'objective', goal: true })
    expect(resolveComposerAction(input({ text: '/goal objective', goalBlocked: true })).blockedReason).toBe('errors.runtime_control.goal_requires_default_mode')
    expect(resolveComposerAction(input({ text: 'objective', goalDraft: true, hasFiles: true })).blockedReason).toBe('chat.goal.textOnly')
    expect(resolveComposerAction(input({ text: 'hello', agentUnavailable: true })).blockedReason).toBe('agent-unavailable')
    expect(resolveComposerAction(input({ text: '/new', agentUnavailable: true })).blockedReason).toBeUndefined()
  })
})
