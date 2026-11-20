import { EventEmitter } from 'node:events'
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  createUtilityRuntimeSession,
  type UtilityFork,
  type UtilityRuntimeChild,
} from './runtime-utility-session'

class FakeUtilityChild extends EventEmitter implements UtilityRuntimeChild {
  readonly postMessage = vi.fn()
  readonly kill = vi.fn(() => {
    this.emit('exit', 1)
    return true
  })
}

const config = {
  serverUrl: 'https://memoh.example.com/',
  key: 'mrk_secret_token_never_put_in_argv_or_env',
  teamId: '33333333-3333-4333-8333-333333333333',
  workspaceBase: '/Users/tester',
  insecureLocalhost: false,
}

afterEach(() => {
  vi.useRealTimers()
})

describe('createUtilityRuntimeSession', () => {
  it('forks the utility worker without credentials in args/env and forwards status, warnings, and errors', async () => {
    const child = new FakeUtilityChild()
    const fork = vi.fn<UtilityFork>(() => child)
    const onStatus = vi.fn()
    const warn = vi.fn()

    const session = createUtilityRuntimeSession(config, { onStatus, warn }, fork)
    const run = session.start()

    expect(fork).toHaveBeenCalledTimes(1)
    const [modulePath, args, forkOptions] = fork.mock.calls[0]!
    expect(modulePath).toMatch(/runtime-worker\.js$/)
    expect(args).toEqual([])
    expect(JSON.stringify(forkOptions)).not.toContain(config.key)

    child.emit('spawn')
    expect(child.postMessage).toHaveBeenCalledWith({ type: 'start', config })

    child.emit('message', { type: 'status', status: 'connecting' })
    child.emit('message', { type: 'status', status: 'connected' })
    child.emit('message', { type: 'status', status: 'disconnected', error: 'transient dial issue' })
    child.emit('message', { type: 'warn', message: 'reconnecting' })

    expect(onStatus).toHaveBeenNthCalledWith(1, 'connecting', undefined)
    expect(onStatus).toHaveBeenNthCalledWith(2, 'connected', undefined)
    expect(onStatus).toHaveBeenNthCalledWith(3, 'disconnected', 'transient dial issue')
    expect(warn).toHaveBeenCalledWith('reconnecting')

    child.emit('message', { type: 'ended' })
    child.emit('exit', 0)
    await expect(run).resolves.toBeUndefined()
  })

  it('stops gracefully and escalates to kill if the utility worker hangs past timeout', async () => {
    vi.useFakeTimers()
    const child = new FakeUtilityChild()
    const session = createUtilityRuntimeSession(config, {}, () => child)
    const run = session.start()

    child.emit('spawn')
    session.stop()
    expect(child.postMessage).toHaveBeenLastCalledWith({ type: 'stop' })
    expect(child.kill).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(3_000)
    expect(child.kill).toHaveBeenCalledTimes(1)
    await expect(run).resolves.toBeUndefined()
  })

  it('rejects start() with worker failure details on unexpected exit', async () => {
    const child = new FakeUtilityChild()
    const session = createUtilityRuntimeSession(config, {}, () => child)
    const run = session.start()

    child.emit('spawn')
    child.emit('message', { type: 'failed', message: 'worker boom' })
    child.emit('exit', 1)

    await expect(run).rejects.toThrow('worker boom')
  })
})
