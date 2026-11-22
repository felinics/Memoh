import {
  RuntimeSession,
  type RuntimeClientConfig,
  type RuntimeSessionStatus,
} from '@memohai/runtime'

type WorkerStartMessage = {
  type: 'start'
  config: RuntimeClientConfig
}

type WorkerStopMessage = {
  type: 'stop'
}

export type RuntimeWorkerInboundMessage = WorkerStartMessage | WorkerStopMessage

export type RuntimeWorkerOutboundMessage =
  | { type: 'status', status: RuntimeSessionStatus, error?: string }
  | { type: 'warn', message: string }
  | { type: 'ended' }
  | { type: 'failed', message: string }

const parentPort = process.parentPort
if (!parentPort) {
  throw new Error('runtime-worker must run inside Electron utilityProcess')
}

let activeSession: RuntimeSession | null = null
let runPromise: Promise<void> | null = null

function send(message: RuntimeWorkerOutboundMessage): void {
  parentPort.postMessage(message)
}

parentPort.on('message', (event) => {
  const payload = event.data as RuntimeWorkerInboundMessage | undefined
  if (!payload || typeof payload !== 'object') return

  if (payload.type === 'start') {
    if (runPromise) return
    const session = new RuntimeSession(payload.config, {
      onStatus: (status, error) => send({ type: 'status', status, error }),
      warn: message => send({ type: 'warn', message }),
    })
    activeSession = session
    runPromise = session.start().then(
      () => {
        send({ type: 'ended' })
        process.exit(0)
      },
      (error: unknown) => {
        const message = error instanceof Error ? error.message : String(error)
        send({ type: 'failed', message })
        process.exit(1)
      },
    )
    return
  }

  if (payload.type === 'stop') {
    activeSession?.stop()
  }
})
