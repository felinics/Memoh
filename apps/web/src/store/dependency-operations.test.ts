import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { createApp, nextTick } from 'vue'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import type { WorkspaceDependencyStreamEvent } from '@/composables/api/useWorkspaceDependencyStream'

const streamDependencyOperation = vi.fn()
const toastSuccess = vi.fn()
const toastError = vi.fn()
const toastWarning = vi.fn()
let router: Router

vi.mock('@/composables/api/useWorkspaceDependencyStream', () => ({
  streamDependencyOperation: (...args: unknown[]) => streamDependencyOperation(...args),
}))

vi.mock('@felinic/ui', () => ({
  toast: {
    success: (...args: unknown[]) => toastSuccess(...args),
    error: (...args: unknown[]) => toastError(...args),
    warning: (...args: unknown[]) => toastWarning(...args),
  },
}))

vi.mock('@/i18n', () => ({
  default: {
    global: {
      t: (key: string, args?: Record<string, unknown>) => (args?.name ? `${key}:${String(args.name)}` : key),
    },
  },
}))

vi.mock('@/router', () => { throw new Error('Shared operations must use the host router') })

const { useDependencyOperationsStore, operationKey } = await import('./dependency-operations')

// A stream the test releases event by event, so "still running" and "settled"
// can be observed as distinct states.
function controlledStream() {
  const queue: Array<(value: IteratorResult<WorkspaceDependencyStreamEvent>) => void> = []
  const pending: IteratorResult<WorkspaceDependencyStreamEvent>[] = []
  const generator: AsyncGenerator<WorkspaceDependencyStreamEvent, void, unknown> = {
    next: () => new Promise((resolve) => {
      const ready = pending.shift()
      if (ready) resolve(ready)
      else queue.push(resolve)
    }),
    return: async () => ({ done: true, value: undefined }),
    throw: async (error: unknown) => { throw error },
    [Symbol.asyncIterator]() { return this },
    [Symbol.asyncDispose]: async () => {},
  }
  function deliver(result: IteratorResult<WorkspaceDependencyStreamEvent>) {
    const waiting = queue.shift()
    if (waiting) waiting(result)
    else pending.push(result)
  }
  return {
    generator,
    emit: (event: WorkspaceDependencyStreamEvent) => deliver({ done: false, value: event }),
    end: () => deliver({ done: true, value: undefined }),
  }
}

async function settleMicrotasks() {
  for (let i = 0; i < 5; i += 1) await nextTick()
}

const codex = { id: 'codex', name: 'Codex', category: 'agent' as const }
const node = { id: 'node', name: 'Node.js', category: 'runtime' as const }

describe('useDependencyOperationsStore', () => {
  beforeEach(() => {
    const pinia = createPinia()
    router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/settings/bots/:botName', name: 'bot-detail', component: { render: () => null } }],
    })
    createApp({ render: () => null }).use(pinia).use(router)
    setActivePinia(pinia)
    streamDependencyOperation.mockReset()
    toastSuccess.mockReset()
    toastError.mockReset()
    toastWarning.mockReset()
  })

  it('keeps the stream alive with no viewer and toasts the verdict with a link to the bot', async () => {
    const stream = controlledStream()
    streamDependencyOperation.mockReturnValue(stream.generator)
    const store = useDependencyOperationsStore()

    const result = store.start({ botId: 'bot-1', targetId: '', item: codex, action: 'install', version: ' 1.2.3 ' })
    expect(result.kind).toBe('started')
    expect(streamDependencyOperation).toHaveBeenCalledWith('bot-1', 'codex', 'install', undefined, expect.objectContaining({ version: '1.2.3' }))

    stream.emit({ type: 'log', stream: 'stderr', data: 'npm warn something very long' })
    await settleMicrotasks()
    const operation = store.get('bot-1', 'codex')
    expect(operation?.status).toBe('running')
    expect(operation?.lines.map(line => line.data)).toEqual(['npm warn something very long'])
    expect(store.runningFor('bot-1')?.key).toBe(operationKey('bot-1', 'codex'))

    stream.emit({ type: 'done', version: 'v1.2.3', entrypoints: { codex: '/opt/codex/bin/codex' } })
    stream.end()
    await settleMicrotasks()

    // Nobody was watching: the outcome landed as a toast and the record is gone.
    expect(store.get('bot-1', 'codex')).toBeUndefined()
    expect(toastSuccess).toHaveBeenCalledTimes(1)
    const [message, options] = toastSuccess.mock.calls[0] as [string, { action?: { label: string; onClick: () => void } }]
    expect(message).toBe('bots.dependencies.background.installed:Codex')
    expect(options.action?.label).toBe('apps.viewBotApps')
    const navigated = new Promise<void>(resolve => router.afterEach(() => resolve()))
    options.action?.onClick()
    await navigated
    expect(router.currentRoute.value.fullPath).toBe('/settings/bots/bot-1?tab=apps')
  })

  it('leaves the verdict to an open dialog and forgets the record when it closes', async () => {
    const stream = controlledStream()
    streamDependencyOperation.mockReturnValue(stream.generator)
    const store = useDependencyOperationsStore()

    const result = store.start({ botId: 'bot-1', targetId: 't-2', item: node, action: 'reinstall' })
    expect(result.kind).toBe('started')
    const key = operationKey('bot-1', 'node')
    store.view(key, 'panel')

    stream.emit({ type: 'done', version: '24.14.0' })
    stream.end()
    await settleMicrotasks()

    const operation = store.get('bot-1', 'node')
    expect(operation?.status).toBe('done')
    expect(operation?.resultVersion).toBe('24.14.0')
    expect(toastSuccess).not.toHaveBeenCalled()

    // A second viewer of the same operation keeps it alive until both close.
    store.view(key, 'supermarket')
    store.unview(key, 'panel')
    expect(store.get('bot-1', 'node')).toBeDefined()
    store.unview(key, 'supermarket')
    expect(store.get('bot-1', 'node')).toBeUndefined()
    expect(toastSuccess).not.toHaveBeenCalled()
  })

  it('reopens the running operation instead of sending a second request for the same dependency', async () => {
    const stream = controlledStream()
    streamDependencyOperation.mockReturnValue(stream.generator)
    const store = useDependencyOperationsStore()

    const first = store.start({ botId: 'bot-1', targetId: '', item: codex, action: 'install' })
    const again = store.start({ botId: 'bot-1', targetId: '', item: codex, action: 'install', version: '9.9.9' })
    expect(again.kind).toBe('running')
    expect(again.kind === 'running' && first.kind === 'started' && again.operation === first.operation).toBe(true)
    expect(streamDependencyOperation).toHaveBeenCalledTimes(1)

    // Another dependency of the same bot is refused while one streams (the
    // Server would answer busy); another bot is not affected.
    const other = store.start({ botId: 'bot-1', targetId: '', item: node, action: 'install' })
    expect(other.kind).toBe('busy')
    expect(other.kind === 'busy' && other.operation.item.id).toBe('codex')
    expect(store.start({ botId: '', targetId: '', item: node, action: 'install' }).kind).toBe('invalid')

    const second = controlledStream()
    streamDependencyOperation.mockReturnValueOnce(second.generator)
    expect(store.start({ botId: 'bot-2', targetId: '', item: node, action: 'install' }).kind).toBe('started')
    expect(streamDependencyOperation).toHaveBeenCalledTimes(2)

    stream.end()
    second.end()
    await settleMicrotasks()
  })

  it('keeps the confirmed revision and source session when retrying a reported script failure', async () => {
    const first = controlledStream()
    streamDependencyOperation.mockReturnValueOnce(first.generator)
    const store = useDependencyOperationsStore()
    const key = operationKey('bot-1', 'codex')

    store.start({ botId: 'bot-1', targetId: '', item: codex, action: 'reinstall', version: '0.1.0', definitionRevision: 'reviewed-revision', sessionId: 'session-1' })
    store.view(key, 'panel')
    first.emit({ type: 'log', stream: 'stdout', data: 'downloading' })
    first.emit({ type: 'error', message: 'script exited 1' })
    first.end()
    await settleMicrotasks()

    const operation = store.get('bot-1', 'codex')
    expect(operation?.status).toBe('error')
    expect(operation?.error).toBe('script exited 1')
    expect(toastError).not.toHaveBeenCalled()

    const second = controlledStream()
    streamDependencyOperation.mockReturnValueOnce(second.generator)
    expect(store.retry(key)).toBe(true)
    expect(operation?.status).toBe('running')
    expect(operation?.lines).toEqual([])
    expect(streamDependencyOperation).toHaveBeenLastCalledWith('bot-1', 'codex', 'reinstall', undefined, expect.objectContaining({ version: '0.1.0', definitionRevision: 'reviewed-revision', sessionId: 'session-1' }))

    // Retry is only for a failed record.
    expect(store.retry(key)).toBe(false)

    second.emit({ type: 'error', message: 'script exited 1' } as WorkspaceDependencyStreamEvent)
    second.end()
    store.unview(key, 'panel')
    await settleMicrotasks()

    // Settled unwatched: the failure is toasted with the localized summary as detail.
    expect(toastError).toHaveBeenCalledWith(
      'bots.dependencies.background.failed:Codex',
      expect.objectContaining({ description: 'script exited 1' }),
    )
    expect(store.get('bot-1', 'codex')).toBeUndefined()
  })

  it('lets the caller replace the background success toast', async () => {
    const stream = controlledStream()
    streamDependencyOperation.mockReturnValue(stream.generator)
    const store = useDependencyOperationsStore()
    const onBackgroundDone = vi.fn()

    store.start({ botId: 'bot-1', targetId: '', item: codex, action: 'install', onBackgroundDone })
    stream.emit({ type: 'done', version: '1.0.0' })
    stream.end()
    await settleMicrotasks()

    expect(onBackgroundDone).toHaveBeenCalledTimes(1)
    expect(onBackgroundDone.mock.calls[0]?.[0]).toMatchObject({ status: 'done', resultVersion: '1.0.0' })
    expect(toastSuccess).not.toHaveBeenCalled()
  })

  it.each(['disconnect', 'server-unknown'])('does not retry an installation with an unconfirmed outcome: %s', async (outcome) => {
    const stream = controlledStream()
    streamDependencyOperation.mockReturnValue(stream.generator)
    const store = useDependencyOperationsStore()
    const key = operationKey('bot-1', 'codex')
    store.start({ botId: 'bot-1', targetId: '', item: codex, action: 'install' })
    store.view(key, 'panel')
    stream.emit({ type: 'started', dependency_id: 'codex' })
    if (outcome === 'server-unknown') {
      stream.emit({ type: 'error', code: 'workspace_dependency_operation_unknown', message: 'result not confirmed' })
    }
    stream.end()
    await settleMicrotasks()
    expect(store.get('bot-1', 'codex')?.status).toBe('unknown')
    expect(store.retry(key)).toBe(false)
    expect(streamDependencyOperation).toHaveBeenCalledTimes(1)
    expect(toastError).not.toHaveBeenCalled()
  })

  it('drops everything silently on reset', async () => {
    const stream = controlledStream()
    streamDependencyOperation.mockReturnValue(stream.generator)
    const store = useDependencyOperationsStore()

    store.start({ botId: 'bot-1', targetId: '', item: codex, action: 'install' })
    expect(store.runningFor('bot-1')).toBeDefined()
    store.reset()
    expect(store.runningFor('bot-1')).toBeUndefined()

    stream.emit({ type: 'done' })
    stream.end()
    await settleMicrotasks()
    expect(toastSuccess).not.toHaveBeenCalled()
    expect(toastError).not.toHaveBeenCalled()
  })
})
