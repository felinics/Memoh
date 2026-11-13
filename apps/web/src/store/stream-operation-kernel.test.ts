// @vitest-environment jsdom
// Failure Modes Covered:
// 1. Unbounded log retention: chatty install/update streams (>2,000 lines) bloating
//    reactive memory instead of ring-truncating at `MAX_LOG_LINES = 2000`.
// 2. Dialog close (`viewers: 1 -> 0`) prematurely aborting an active stream, or
//    failing to surface background completion (`done` / `error` / `unknown`) Toasts
//    and evict the finished record once settled.
// 3. Double notification: firing background Toasts while a dialog is actively
//    watching (`viewers > 0`), or leaking finished records after the last viewer closes.
// 4. Dropped SSE stream without terminal event: `pollUntilReconciled` failing to
//    poll until server state settles, or hanging past `maxMs` / `AbortSignal`.
// 5. Per-bot concurrency race: starting a second operation on the same `botId`
//    dispatching duplicate backend runs instead of returning `{ kind: 'busy' }`.
// 6. Cross-account leak on logout (`notifyAuthSessionCleared`): leaving in-flight
//    streams un-aborted, retaining stale operations, or firing late Toasts after logout.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { notifyAuthSessionCleared } from '@/lib/auth-session'
import {
  createStreamOperationKernel,
  pollUntilReconciled,
  type BaseStreamOperation,
} from './stream-operation-kernel'

const toastMocks = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
  invalidate: vi.fn(),
}))

vi.mock('@felinic/ui', () => ({
  toast: {
    success: toastMocks.success,
    error: toastMocks.error,
    warning: toastMocks.warning,
  },
}))

vi.mock('@pinia/colada', () => ({
  useQueryCache: () => ({ invalidateQueries: toastMocks.invalidate }),
}))

interface TestOperation extends BaseStreamOperation {
  label: string
  reconciled: boolean
}

function deferred<T = void>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

describe('stream-operation-kernel & pollUntilReconciled', () => {
  let router: Router

  beforeEach(() => {
    vi.useFakeTimers()
    vi.clearAllMocks()
    router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/bots/:botName', name: 'bot-detail', component: { render: () => null } }],
    })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  function makeOp(key: string, botId: string, label = 'Target'): TestOperation {
    return reactive<TestOperation>({
      key,
      botId,
      label,
      reconciled: false,
      status: 'running',
      lines: [],
      error: '',
    })
  }

  it('caps retained log lines at 2,000 while preserving monotonic order of the newest lines', async () => {
    const gate = deferred()
    const kernel = createStreamOperationKernel<TestOperation>({
      router,
      actionLabel: () => 'View',
      unknownTitle: op => `Unknown: ${op.label}`,
      doneNotice: op => ({ message: `Done: ${op.label}` }),
      failedTitle: op => `Failed: ${op.label}`,
      resetForRun: () => {},
      invalidateOnSettle: () => {},
      consume: async (op, _signal, { pushLine }) => {
        for (let i = 0; i < 2050; i++) {
          pushLine('stdout', `line-${i}`)
        }
        await gate.promise
        op.status = 'done'
      },
    })

    const res = kernel.startOperation({
      key: 'bot-1/dep-a',
      botId: 'bot-1',
      create: () => makeOp('bot-1/dep-a', 'bot-1'),
    })
    expect(res.kind).toBe('started')
    kernel.view('bot-1/dep-a', 'dialog-1')

    const op = kernel.getByKey('bot-1/dep-a')!
    expect(op.lines).toHaveLength(2000)
    expect(op.lines[0]?.data).toBe('line-50')
    expect(op.lines[1999]?.data).toBe('line-2049')
    expect((op.lines[1999]?.id as number) > (op.lines[0]?.id as number)).toBe(true)

    gate.resolve()
    await vi.advanceTimersByTimeAsync(0)
    kernel.reset()
  })

  it.each([
    ['done', 'success'] as const,
    ['error', 'error'] as const,
    ['unknown', 'warning'] as const,
  ])('continues running when dialog closes (viewers 1 -> 0) and emits %s background toast on settle', async (outcome, expectedToast) => {
    const gate = deferred()
    let capturedSignal: AbortSignal | undefined
    const kernel = createStreamOperationKernel<TestOperation>({
      router,
      actionLabel: () => 'Open Apps',
      unknownTitle: op => `Unknown: ${op.label}`,
      doneNotice: op => ({ message: `Done: ${op.label}` }),
      failedTitle: op => `Failed: ${op.label}`,
      resetForRun: () => {},
      invalidateOnSettle: toastMocks.invalidate,
      consume: async (op, signal) => {
        capturedSignal = signal
        await gate.promise
        op.status = outcome
        if (outcome !== 'done') op.error = `reason-${outcome}`
      },
    })

    kernel.startOperation({
      key: 'bot-1/app-x',
      botId: 'bot-1',
      create: () => makeOp('bot-1/app-x', 'bot-1', 'AppX'),
    })
    kernel.view('bot-1/app-x', 'dialog')
    expect(kernel.isViewed('bot-1/app-x')).toBe(true)

    // Closing dialog while running must NOT abort or evict yet
    kernel.unview('bot-1/app-x', 'dialog')
    expect(kernel.isViewed('bot-1/app-x')).toBe(false)
    expect(capturedSignal?.aborted).toBe(false)
    expect(kernel.getByKey('bot-1/app-x')?.status).toBe('running')

    gate.resolve()
    await vi.advanceTimersByTimeAsync(0)

    expect(toastMocks.invalidate).toHaveBeenCalledTimes(1)
    expect(toastMocks[expectedToast]).toHaveBeenCalledTimes(1)
    // Settled with 0 viewers -> record evicted immediately after notifying
    expect(kernel.getByKey('bot-1/app-x')).toBeUndefined()
  })

  it('suppresses background toast while watched (viewers > 0) and evicts silently once all viewers close', async () => {
    const gate = deferred()
    const kernel = createStreamOperationKernel<TestOperation>({
      router,
      actionLabel: () => 'View',
      unknownTitle: op => `Unknown: ${op.label}`,
      doneNotice: op => ({ message: `Done: ${op.label}` }),
      failedTitle: op => `Failed: ${op.label}`,
      resetForRun: () => {},
      invalidateOnSettle: toastMocks.invalidate,
      consume: async (op) => {
        await gate.promise
        op.status = 'done'
      },
    })

    kernel.startOperation({
      key: 'bot-1/app-x',
      botId: 'bot-1',
      create: () => makeOp('bot-1/app-x', 'bot-1'),
    })
    kernel.view('bot-1/app-x', 'dialog-a')
    kernel.view('bot-1/app-x', 'dialog-b')

    gate.resolve()
    await vi.advanceTimersByTimeAsync(0)

    expect(toastMocks.success).not.toHaveBeenCalled()
    expect(toastMocks.error).not.toHaveBeenCalled()
    expect(toastMocks.warning).not.toHaveBeenCalled()
    expect(kernel.getByKey('bot-1/app-x')?.status).toBe('done')

    kernel.unview('bot-1/app-x', 'dialog-a')
    expect(kernel.getByKey('bot-1/app-x')).toBeDefined()
    kernel.unview('bot-1/app-x', 'dialog-b')
    expect(kernel.getByKey('bot-1/app-x')).toBeUndefined()
    expect(toastMocks.success).not.toHaveBeenCalled()
  })

  it('reconciles unexpected SSE EOF via pollUntilReconciled and stops cleanly on deadline or abort', async () => {
    let checks = 0
    const kernel = createStreamOperationKernel<TestOperation>({
      router,
      actionLabel: () => 'View',
      unknownTitle: op => `Unknown: ${op.label}`,
      doneNotice: op => ({ message: `Done: ${op.label}` }),
      failedTitle: op => `Failed: ${op.label}`,
      resetForRun: (op) => { op.reconciled = false },
      invalidateOnSettle: toastMocks.invalidate,
      consume: async (op, signal, { pushLine }) => {
        pushLine('stderr', 'stream dropped, reconciling')
        const ok = await pollUntilReconciled({
          intervalMs: 1000,
          maxMs: 5000,
          signal,
          check: async () => {
            checks += 1
            if (checks >= 3) {
              op.reconciled = true
              op.status = 'done'
              return true
            }
            return false
          },
        })
        if (!ok && !signal.aborted) op.status = 'unknown'
      },
    })

    kernel.startOperation({
      key: 'bot-1/app-eof',
      botId: 'bot-1',
      create: () => makeOp('bot-1/app-eof', 'bot-1', 'EofApp'),
    })
    kernel.view('bot-1/app-eof', 'dialog')

    await vi.advanceTimersByTimeAsync(2000)
    expect(checks).toBe(2)
    expect(kernel.getByKey('bot-1/app-eof')?.status).toBe('running')

    await vi.advanceTimersByTimeAsync(1000)
    expect(checks).toBe(3)
    expect(kernel.getByKey('bot-1/app-eof')).toMatchObject({ status: 'done', reconciled: true })

    // Deadline expiry returns false when never reconciled
    const controller = new AbortController()
    const timedOut = pollUntilReconciled({
      intervalMs: 1000,
      maxMs: 2500,
      signal: controller.signal,
      check: async () => false,
    })
    await vi.advanceTimersByTimeAsync(3000)
    expect(await timedOut).toBe(false)
    kernel.reset()
  })

  it('enforces single active operation per bot while allowing reopening same key and parallel runs on other bots', async () => {
    const consumeCalls: string[] = []
    const gates = new Map<string, ReturnType<typeof deferred<void>>>()
    const kernel = createStreamOperationKernel<TestOperation>({
      router,
      actionLabel: () => 'View',
      unknownTitle: op => op.label,
      doneNotice: op => ({ message: op.label }),
      failedTitle: op => op.label,
      resetForRun: () => {},
      invalidateOnSettle: () => {},
      consume: async (op) => {
        consumeCalls.push(op.key)
        const gate = deferred()
        gates.set(op.key, gate)
        await gate.promise
        op.status = 'done'
      },
    })

    const first = kernel.startOperation({
      key: 'bot-1/op-1',
      botId: 'bot-1',
      create: () => makeOp('bot-1/op-1', 'bot-1'),
    })
    expect(first.kind).toBe('started')

    // Reopening identical key returns `running` without re-running consume
    const reopened = kernel.startOperation({
      key: 'bot-1/op-1',
      botId: 'bot-1',
      create: () => makeOp('bot-1/op-1', 'bot-1'),
    })
    expect(reopened.kind).toBe('running')

    // Different key on same bot returns `busy`
    const blocked = kernel.startOperation({
      key: 'bot-1/op-2',
      botId: 'bot-1',
      create: () => makeOp('bot-1/op-2', 'bot-1'),
    })
    expect(blocked.kind).toBe('busy')
    if (blocked.kind === 'busy') expect(blocked.operation.key).toBe('bot-1/op-1')

    // Different bot proceeds independently
    const otherBot = kernel.startOperation({
      key: 'bot-2/op-1',
      botId: 'bot-2',
      create: () => makeOp('bot-2/op-1', 'bot-2'),
    })
    expect(otherBot.kind).toBe('started')
    expect(consumeCalls).toEqual(['bot-1/op-1', 'bot-2/op-1'])

    kernel.reset()
  })

  it('aborts in-flight streams immediately and suppresses late toasts/invalidation on auth session clear', async () => {
    const gate = deferred()
    let activeSignal: AbortSignal | undefined
    const kernel = createStreamOperationKernel<TestOperation>({
      router,
      actionLabel: () => 'View',
      unknownTitle: op => op.label,
      doneNotice: op => ({ message: `Done: ${op.label}` }),
      failedTitle: op => op.label,
      resetForRun: () => {},
      invalidateOnSettle: toastMocks.invalidate,
      consume: async (op, signal) => {
        activeSignal = signal
        await gate.promise
        op.status = 'done'
      },
    })

    kernel.startOperation({
      key: 'bot-1/op-auth',
      botId: 'bot-1',
      create: () => makeOp('bot-1/op-auth', 'bot-1', 'SecretOp'),
    })
    expect(kernel.runningFor('bot-1')).toBeDefined()
    expect(activeSignal?.aborted).toBe(false)

    // Real auth session clear event (e.g. logout / 401)
    notifyAuthSessionCleared('logout')

    expect(activeSignal?.aborted).toBe(true)
    expect(kernel.runningFor('bot-1')).toBeUndefined()
    expect(kernel.getByKey('bot-1/op-auth')).toBeUndefined()

    // Even if the underlying promise resolves afterwards, settle must not fire
    gate.resolve()
    await vi.advanceTimersByTimeAsync(0)
    expect(toastMocks.success).not.toHaveBeenCalled()
    expect(toastMocks.invalidate).not.toHaveBeenCalled()
  })
})
