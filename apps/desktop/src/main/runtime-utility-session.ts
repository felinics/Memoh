import { utilityProcess } from 'electron'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { RuntimeClientConfig, RuntimeSessionOptions } from '@memohai/runtime'
import type { ManagedRuntimeSession } from './remote-runtime'
import type { RuntimeWorkerInboundMessage, RuntimeWorkerOutboundMessage } from './runtime-worker'

const STOP_KILL_TIMEOUT_MS = 3_000

export interface UtilityRuntimeChild {
  on(event: 'spawn', listener: () => void): this
  on(event: 'message', listener: (message: unknown) => void): this
  on(event: 'exit', listener: (code: number) => void): this
  postMessage(message: RuntimeWorkerInboundMessage): void
  kill(): boolean
}

export type UtilityFork = (
  modulePath: string,
  args?: string[],
  options?: Electron.ForkOptions,
) => UtilityRuntimeChild

export function createUtilityRuntimeSession(
  config: RuntimeClientConfig,
  options: RuntimeSessionOptions,
  forkProcess: UtilityFork = (modulePath, args, forkOptions) => utilityProcess.fork(modulePath, args, forkOptions),
): ManagedRuntimeSession {
  const workerPath = join(dirname(fileURLToPath(import.meta.url)), 'runtime-worker.js')
  let child: UtilityRuntimeChild | null = null
  let spawned = false
  let stopping = false
  let stopTimer: ReturnType<typeof setTimeout> | null = null
  let settled = false

  return {
    start() {
      if (stopping) return Promise.resolve()
      return new Promise<void>((resolve, reject) => {
        let lastFailure = ''

        const clearStopTimer = () => {
          if (stopTimer) {
            clearTimeout(stopTimer)
            stopTimer = null
          }
        }

        const finishResolve = () => {
          if (settled) return
          settled = true
          clearStopTimer()
          resolve()
        }

        const finishReject = (message: string) => {
          if (settled) return
          settled = true
          clearStopTimer()
          reject(new Error(message))
        }

        const proc = forkProcess(workerPath, [], { serviceName: 'Memoh Local Runtime' })
        child = proc

        proc.on('spawn', () => {
          spawned = true
          if (stopping) {
            proc.postMessage({ type: 'stop' })
            return
          }
          // Decrypted credentials travel only over IPC after spawn, never via argv or env.
          proc.postMessage({ type: 'start', config })
        })

        proc.on('message', (raw) => {
          const msg = raw as RuntimeWorkerOutboundMessage | undefined
          if (!msg || typeof msg !== 'object') return
          switch (msg.type) {
            case 'status':
              options.onStatus?.(msg.status, msg.error)
              break
            case 'warn':
              options.warn?.(msg.message)
              break
            case 'failed':
              lastFailure = msg.message
              break
            case 'ended':
              break
          }
        })

        proc.on('exit', (code) => {
          child = null
          if (stopping || code === 0) {
            finishResolve()
            return
          }
          finishReject(lastFailure || `local runtime process exited with code ${code}`)
        })
      })
    },

    stop() {
      if (stopping) return
      stopping = true
      const proc = child
      if (!proc || settled) return
      if (spawned) {
        proc.postMessage({ type: 'stop' })
      }
      stopTimer = setTimeout(() => {
        if (!settled) {
          proc.kill()
        }
      }, STOP_KILL_TIMEOUT_MS)
      stopTimer.unref?.()
    },
  }
}
