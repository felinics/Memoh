import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, defineStore, setActivePinia } from 'pinia'
import { computed, ref } from 'vue'
import type { AcpagentRuntimeStatus, AcpclientAvailableCommandInfo } from '@memohai/sdk'
import { useACPRuntime } from './useACPRuntime'

const chatStoreMock = vi.hoisted(() => ({
  use: vi.fn(),
}))

vi.mock('@/store/chat-list', () => ({
  useChatStore: chatStoreMock.use,
}))

function runtime(
  model: string,
  effort: string,
  agentId = 'codex',
  sessionId = 'session-1',
  availableCommands: AcpclientAvailableCommandInfo[] = [],
): AcpagentRuntimeStatus {
  return {
    runtime_id: 'runtime-1',
    session_id: sessionId,
    agent_id: agentId,
    state: 'idle',
    available_commands: availableCommands,
    models: {
      current_model_id: model,
      available_models: [
        { id: 'model-a', name: 'Model A' },
        { id: 'model-b', name: 'Model B' },
      ],
    },
    reasoning: {
      current_effort: effort,
      available_efforts: [{ id: effort, name: effort }],
    },
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

function makeStore(initial?: AcpagentRuntimeStatus) {
  const ensureACPRuntime = vi.fn<() => Promise<AcpagentRuntimeStatus | undefined>>()
  const setACPRuntimeMode = vi.fn<(modeId: string) => Promise<AcpagentRuntimeStatus | undefined>>()
  const setACPRuntimeModel = vi.fn<(modelId: string) => Promise<AcpagentRuntimeStatus | undefined>>()
  const setACPRuntimeReasoning = vi.fn<(effort: string) => Promise<AcpagentRuntimeStatus | undefined>>()
  const pendingExternalAgentStateFor = vi.fn(() => null as unknown)
  const useStore = defineStore(`acp-runtime-test-${Math.random()}`, () => {
    const acpRuntimeStatuses = ref<Record<string, AcpagentRuntimeStatus | undefined>>({
      ...(initial ? { 'bot-1:session-1': initial } : {}),
    })
    const acpRuntimePending = ref<Record<string, boolean>>({})
    return {
      acpRuntimeStatuses,
      acpRuntimePending,
      acpRuntimeKey: (botId: string, sessionId: string) => `${botId.trim()}:${sessionId.trim()}`,
      pendingExternalAgentStateFor,
      ensurePendingACPRuntime: vi.fn(),
      setPendingACPModel: vi.fn(),
      setPendingACPReasoning: vi.fn(),
      ensureACPRuntime,
      setACPRuntimeMode,
      setACPRuntimeModel,
      setACPRuntimeReasoning,
    }
  })
  const store = useStore()
  chatStoreMock.use.mockReturnValue(store)
  return { store, ensureACPRuntime, setACPRuntimeModel, setACPRuntimeReasoning, pendingExternalAgentStateFor }
}

function useSessionRuntime() {
  return useACPRuntime({
    target: computed(() => ({ botId: 'bot-1', sessionId: 'session-1', viewId: 'chat' })),
    pending: false,
    enabled: true,
    agentId: 'codex',
    projectPath: '',
  })
}

describe('useACPRuntime', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    chatStoreMock.use.mockReset()
  })

  it('exposes Agent commands only for the current runtime target', async () => {
    makeStore(runtime('model-a', 'high', 'codex', 'session-1', [
      { name: 'review', description: 'Review' },
    ]))
    const sessionId = ref('session-1')
    const pane = useACPRuntime({
      target: computed(() => ({ botId: 'bot-1', sessionId: sessionId.value, viewId: 'chat' })),
      pending: false,
      enabled: true,
      agentId: 'codex',
      projectPath: '',
    })

    expect(pane.availableCommands.value.map(command => command.name)).toEqual(['review'])
    sessionId.value = 'session-2'
    await Promise.resolve()
    expect(pane.availableCommands.value).toEqual([])
  })

  it('adopts only the latest model response', async () => {
    const { setACPRuntimeModel } = makeStore(runtime('model-a', 'high'))
    const first = deferred<AcpagentRuntimeStatus | undefined>()
    const second = deferred<AcpagentRuntimeStatus | undefined>()
    setACPRuntimeModel
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise)
    const pane = useSessionRuntime()

    const older = pane.setModel('model-a')
    const newer = pane.setModel('model-b')
    second.resolve(runtime('model-b', 'low'))
    await newer
    first.resolve(runtime('model-a', 'high'))
    await older

    expect(pane.currentModelId.value).toBe('model-b')
    expect(pane.currentReasoningEffort.value).toBe('low')
  })

  it('updates the runtime reasoning effort immediately', async () => {
    const { setACPRuntimeReasoning } = makeStore(runtime('model-a', 'high'))
    setACPRuntimeReasoning.mockResolvedValueOnce(runtime('model-a', 'low'))
    const pane = useSessionRuntime()

    await pane.setReasoning('low')

    expect(setACPRuntimeReasoning).toHaveBeenCalledWith('low', 'session-1')
    expect(pane.currentReasoningEffort.value).toBe('low')
  })

  it('rebinds draft capability to the target Session on promotion or navigation', async () => {
    const { store, pendingExternalAgentStateFor } = makeStore()
    const pending = ref(true)
    const sessionId = ref<string | null>(null)
    pendingExternalAgentStateFor.mockReturnValue({
      runtimeStatus: runtime('model-b', 'low'),
      ensuring: false,
    })
    const pane = useACPRuntime({
      target: computed(() => ({ botId: 'bot-1', sessionId: sessionId.value, viewId: 'chat' })),
      pending,
      enabled: true,
      agentId: 'codex',
      projectPath: '',
    })

    expect(pane.currentModelId.value).toBe('model-b')
    store.acpRuntimeStatuses = {
      'bot-1:session-1': runtime('model-a', 'high'),
    }
    sessionId.value = 'session-1'
    pending.value = false
    await Promise.resolve()

    expect(pane.currentModelId.value).toBe('model-a')
    expect(pane.currentReasoningEffort.value).toBe('high')
  })

  it('does not reuse a shared snapshot from the previous Agent', async () => {
    const { store, ensureACPRuntime } = makeStore(runtime('model-a', 'high', 'codex'))
    const agentId = ref('codex')
    const pane = useACPRuntime({
      target: computed(() => ({ botId: 'bot-1', sessionId: 'session-1', viewId: 'chat' })),
      pending: false,
      enabled: true,
      agentId,
      projectPath: '',
    })

    agentId.value = 'claude-code'
    await Promise.resolve()
    expect(pane.runtime.value).toBeUndefined()

    ensureACPRuntime.mockImplementationOnce(async () => {
      const status = runtime('model-b', 'low', 'claude-code')
      store.acpRuntimeStatuses = { 'bot-1:session-1': status }
      return status
    })
    await pane.ensure()

    expect(ensureACPRuntime).toHaveBeenCalledWith('session-1')
    expect(pane.runtime.value?.agent_id).toBe('claude-code')
    expect(pane.currentModelId.value).toBe('model-b')
  })

  it('does not let a late ensure patch the model on a new target', async () => {
    const { store, ensureACPRuntime, setACPRuntimeModel } = makeStore()
    const firstEnsure = deferred<AcpagentRuntimeStatus | undefined>()
    ensureACPRuntime.mockReturnValueOnce(firstEnsure.promise)
    const sessionId = ref('session-1')
    const pane = useACPRuntime({
      target: computed(() => ({ botId: 'bot-1', sessionId: sessionId.value, viewId: 'chat' })),
      pending: false,
      enabled: true,
      agentId: 'codex',
      projectPath: '',
    })

    const stale = pane.ensure()
    store.acpRuntimeStatuses = {
      'bot-1:session-2': runtime('model-a', 'high', 'codex', 'session-2'),
    }
    sessionId.value = 'session-2'
    await Promise.resolve()
    firstEnsure.resolve(runtime('model-a', 'high', 'codex', 'session-1'))

    await expect(stale).resolves.toBeUndefined()
    expect(setACPRuntimeModel).not.toHaveBeenCalled()
    expect(pane.runtime.value?.session_id).toBe('session-2')
  })

  it('restores the selected model when a refreshed runtime starts on its default', async () => {
    const { ensureACPRuntime, setACPRuntimeModel } = makeStore(runtime('model-b', 'low'))
    ensureACPRuntime.mockResolvedValueOnce(runtime('model-a', 'high'))
    setACPRuntimeModel.mockResolvedValueOnce(runtime('model-b', 'low'))
    const pane = useSessionRuntime()

    await pane.ensure(true, 'model-b')

    expect(setACPRuntimeModel).toHaveBeenCalledWith('model-b', 'session-1')
    expect(pane.currentModelId.value).toBe('model-b')
    expect(pane.currentReasoningEffort.value).toBe('low')
  })

  it('clears preparation after a setter supersedes an in-flight ensure', async () => {
    const { ensureACPRuntime, setACPRuntimeModel } = makeStore(runtime('model-a', 'high'))
    const pendingEnsure = deferred<AcpagentRuntimeStatus | undefined>()
    ensureACPRuntime.mockReturnValueOnce(pendingEnsure.promise)
    setACPRuntimeModel.mockResolvedValueOnce(runtime('model-b', 'low'))
    const pane = useSessionRuntime()

    const preparing = pane.ensure(true)
    expect(pane.isPreparing.value).toBe(true)
    await pane.setModel('model-b')

    pendingEnsure.resolve(runtime('model-a', 'high'))
    await preparing

    expect(pane.isPreparing.value).toBe(false)
    expect(pane.currentModelId.value).toBe('model-b')
  })

})
