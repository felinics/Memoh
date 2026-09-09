import { describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import type { AcpagentRuntimeStatus } from '@memohai/sdk'
import {
  createACPRuntimeRegistry,
  type ACPRuntimeRegistryTransport,
} from './acp-runtime-registry'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function runtime(id: string): AcpagentRuntimeStatus {
  return {
    runtime_id: id,
    agent_id: 'codex',
    state: 'idle',
  }
}

function makeRegistry() {
  const currentBotId = ref<string | null>('bot-1')
  const sessionId = ref<string | null>('session-1')
  const transport: ACPRuntimeRegistryTransport = {
    ensureACPRuntime: vi.fn(),
    setACPRuntimeMode: vi.fn(),
    setACPRuntimeModel: vi.fn(),
    setACPRuntimeReasoning: vi.fn(),
  }
  return {
    currentBotId,
    sessionId,
    transport,
    registry: createACPRuntimeRegistry({ currentBotId, sessionId }, transport),
  }
}

describe('ACP runtime registry', () => {
  it('normalizes keys and owns immutable status updates', () => {
    const { registry } = makeRegistry()
    expect(registry.acpRuntimeKey(' bot-1 ', ' session-1 ')).toBe('bot-1:session-1')
    expect(registry.acpRuntimeKey('', 'session-1')).toBe('')

    const status = runtime('runtime-1')
    registry.setACPRuntimeStatus('bot-1', 'session-1', status)
    expect(registry.acpRuntimeStatuses.value).toEqual({ 'bot-1:session-1': status })

    registry.clearACPRuntimeStatus('bot-1', 'session-1')
    expect(registry.acpRuntimeStatuses.value).toEqual({})
  })

  it('deduplicates concurrent ensure requests and clears pending on completion', async () => {
    const { registry, transport } = makeRegistry()
    const request = deferred<AcpagentRuntimeStatus>()
    vi.mocked(transport.ensureACPRuntime).mockReturnValue(request.promise)

    const first = registry.ensureACPRuntime()
    const second = registry.ensureACPRuntime('session-1')
    expect(transport.ensureACPRuntime).toHaveBeenCalledOnce()
    expect(registry.acpRuntimePending.value).toEqual({ 'bot-1:session-1': true })

    const status = runtime('runtime-1')
    request.resolve(status)
    await expect(first).resolves.toBe(status)
    await expect(second).resolves.toBe(status)
    expect(registry.acpRuntimeStatuses.value).toEqual({ 'bot-1:session-1': status })
    expect(registry.acpRuntimePending.value).toEqual({})
  })

  it('does not restore a request cleared before its response arrives', async () => {
    const { registry, transport } = makeRegistry()
    const request = deferred<AcpagentRuntimeStatus>()
    vi.mocked(transport.ensureACPRuntime).mockReturnValue(request.promise)

    const pending = registry.ensureACPRuntime()
    registry.clearACPRuntimeStatus('bot-1', 'session-1')
    request.resolve(runtime('late-runtime'))
    await pending

    expect(registry.acpRuntimeStatuses.value).toEqual({})
    expect(registry.acpRuntimePending.value).toEqual({})
  })

  it('resets user-scoped state and rejects late writes from old requests', async () => {
    const { registry, transport } = makeRegistry()
    const request = deferred<AcpagentRuntimeStatus>()
    vi.mocked(transport.ensureACPRuntime).mockReturnValue(request.promise)

    const pending = registry.ensureACPRuntime()
    registry.resetExternalAgentRuntimeRegistry()
    request.resolve(runtime('old-user-runtime'))
    await pending

    expect(registry.acpRuntimeStatuses.value).toEqual({})
    expect(registry.acpRuntimePending.value).toEqual({})
  })

  it('updates the selected session model through the registry', async () => {
    const { registry, transport } = makeRegistry()
    const status = runtime('runtime-model')
    vi.mocked(transport.setACPRuntimeModel).mockResolvedValue(status)

    await expect(registry.setACPRuntimeModel('gpt-5')).resolves.toBe(status)
    expect(transport.setACPRuntimeModel).toHaveBeenCalledWith('bot-1', 'session-1', 'gpt-5')
    expect(registry.acpRuntimeStatuses.value).toEqual({ 'bot-1:session-1': status })
  })

  it('keeps the latest model response when requests resolve out of order', async () => {
    const { registry, transport } = makeRegistry()
    const firstRequest = deferred<AcpagentRuntimeStatus>()
    const secondRequest = deferred<AcpagentRuntimeStatus>()
    vi.mocked(transport.setACPRuntimeModel)
      .mockReturnValueOnce(firstRequest.promise)
      .mockReturnValueOnce(secondRequest.promise)

    const first = registry.setACPRuntimeModel('older-model')
    const second = registry.setACPRuntimeModel('newer-model')
    expect(transport.setACPRuntimeModel).toHaveBeenCalledTimes(2)

    const newer = runtime('newer-runtime')
    secondRequest.resolve(newer)
    await second
    firstRequest.resolve(runtime('older-runtime'))
    await first

    expect(registry.acpRuntimeStatuses.value).toEqual({ 'bot-1:session-1': newer })
  })

  it('updates the selected session reasoning effort through the registry', async () => {
    const { registry, transport } = makeRegistry()
    const status = runtime('runtime-reasoning')
    vi.mocked(transport.setACPRuntimeReasoning).mockResolvedValue(status)

    await expect(registry.setACPRuntimeReasoning('low')).resolves.toBe(status)
    expect(transport.setACPRuntimeReasoning).toHaveBeenCalledWith('bot-1', 'session-1', 'low')
    expect(registry.acpRuntimeStatuses.value).toEqual({ 'bot-1:session-1': status })
  })

  it('does not restore a model response after the session cache is cleared', async () => {
    const { registry, transport } = makeRegistry()
    const request = deferred<AcpagentRuntimeStatus>()
    vi.mocked(transport.setACPRuntimeModel).mockReturnValue(request.promise)

    const pending = registry.setACPRuntimeModel('gpt-5')
    registry.clearACPRuntimeStatus('bot-1', 'session-1')
    request.resolve(runtime('late-model-runtime'))
    await pending

    expect(registry.acpRuntimeStatuses.value).toEqual({})
  })
})
