import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'
import { constants as osConstants } from 'node:os'
import type { Readable } from 'node:stream'

import { status, type ServerDuplexStream } from '@grpc/grpc-js'

import { mapNodeError, rpcError } from '../rpc'
import type { ExecInput, ExecOutput } from '../types'
import { guardedEnvironment } from './guards'
import type { ResolvePathOptions } from './paths'
import { shellSpawnSpec } from './shell'

export interface ExecPathResolver {
  defaultDirectory: string
  resolve(path: string, options?: ResolvePathOptions): Promise<string>
  revalidate(path: string, options?: ResolvePathOptions): Promise<string>
}

export interface ExecChildSupervisor {
  register(child: ChildProcessWithoutNullStreams): Promise<void>
  terminate(child: ChildProcessWithoutNullStreams): Promise<void>
}

const stdoutStream = 0
const stderrStream = 1
const exitStream = 2
const defaultTimeoutSeconds = 30

export class WorkspaceExecService {
  constructor(
    private readonly paths: ExecPathResolver,
    private readonly children: ExecChildSupervisor,
    private readonly acceptingRPCs: () => boolean = () => true,
    private readonly trustedExecutableDirectories: readonly string[] = [],
  ) {}

  exec(call: ServerDuplexStream<ExecInput, ExecOutput>): void {
    let first = true
    let inputEnded = false
    let cancelled = false
    let terminalSent = false
    let child: ChildProcessWithoutNullStreams | undefined
    // stdin frames must reach the child in arrival order. Everything queues
    // until start() has flushed the queue; only then do writes go direct.
    // Writing directly as soon as `child` exists would let frames arriving in
    // the async spawn window overtake the queued first-message bytes.
    let stdinReady = false
    const queuedInput: Buffer[] = []
    const admissionActive = () => this.acceptingRPCs() && !cancelled && !call.cancelled && !call.destroyed

    const terminate = () => {
      if (terminalSent) {
        return
      }
      cancelled = true
      if (child) {
        void this.children.terminate(child)
      }
    }

    call.on('cancelled', terminate)
    call.on('error', terminate)
    call.on('end', () => {
      inputEnded = true
      if (first) {
        first = false
        if (!admissionActive()) {
          return
        }
        const error = rpcError(status.INVALID_ARGUMENT, 'failed to receive exec config')
        call.emit('error', error)
        return
      }
      // Before the flush point, start() observes inputEnded via
      // shouldEndInput() and ends stdin after flushing the queue; ending it
      // here would drop the still-queued bytes.
      if (stdinReady) {
        child?.stdin.end()
      }
    })
    call.on('data', (message: ExecInput) => {
      if (first) {
        first = false
        if (message.stdin_data?.length) {
          queuedInput.push(Buffer.from(message.stdin_data))
        }
        void this.start(
          call,
          message,
          queuedInput,
          () => inputEnded || cancelled,
          () => { terminalSent = true },
          admissionActive,
          spawned => {
            child = spawned
            if (!admissionActive()) {
              void this.children.terminate(spawned)
            }
          },
          () => { stdinReady = true },
        )
          .catch(error => {
            if (admissionActive()) {
              const mapped = mapNodeError(error, 'exec')
              call.emit('error', mapped)
            }
          })
        return
      }
      if (message.stdin_data?.length) {
        const data = Buffer.from(message.stdin_data)
        if (child && stdinReady) {
          child.stdin.write(data)
        } else {
          queuedInput.push(data)
        }
      }
    })
  }

  private async start(
    call: ServerDuplexStream<ExecInput, ExecOutput>,
    request: ExecInput,
    queuedInput: Buffer[],
    shouldEndInput: () => boolean,
    markTerminalSent: () => void,
    admissionActive: () => boolean,
    onSpawn: (child: ChildProcessWithoutNullStreams) => void,
    onStdinReady: () => void,
  ): Promise<ChildProcessWithoutNullStreams> {
    assertExecAdmissionActive(call, admissionActive)
    if (request.pty) {
      throw rpcError(status.UNIMPLEMENTED, 'PTY is not implemented by Remote Runtime M1')
    }
    const command = request.command?.trim()
    if (!command) {
      throw rpcError(status.INVALID_ARGUMENT, 'command is required')
    }
    let workDirectory = this.paths.defaultDirectory
    if (request.work_dir?.trim()) {
      assertExecAdmissionActive(call, admissionActive)
      workDirectory = await this.paths.resolve(request.work_dir, { requireDirectory: true })
      assertExecAdmissionActive(call, admissionActive)
    }
    assertExecAdmissionActive(call, admissionActive)
    await this.paths.revalidate(workDirectory, { requireDirectory: true })
    assertExecAdmissionActive(call, admissionActive)
    const environment = guardedEnvironment(request.env, {
      clean: request.clean_env,
      unset: request.unset_env,
      trustedExecutableDirectories: this.trustedExecutableDirectories,
    })

    assertExecAdmissionActive(call, admissionActive)
    const shell = shellSpawnSpec(command)
    const child = spawn(shell.command, shell.args, {
      cwd: workDirectory,
      env: environment,
      detached: shell.detached,
      shell: shell.shell,
      stdio: ['pipe', 'pipe', 'pipe'],
      windowsHide: shell.windowsHide,
    })
    onSpawn(child)
    // Registration starts immediately after spawn() and adds the PID/PGID to
    // the supervisor synchronously before returning its promise.
    // close() therefore cannot miss a process in the spawn-event window.
    const registration = this.children.register(child)
    void registration.catch(() => undefined)
    try {
      await new Promise<void>((resolve, reject) => {
        child.once('spawn', resolve)
        child.once('error', reject)
      })
      await registration
    } catch (error) {
      await this.children.terminate(child)
      throw error
    }
    if (!admissionActive()) {
      await this.children.terminate(child)
      throw rpcError(status.CANCELLED, 'exec was cancelled before process admission completed')
    }
    child.stdin.on('error', () => undefined)
    const closed = new Promise<{ code: number | null, signal: NodeJS.Signals | null }>(resolve => {
      child.once('close', (code, signal) => resolve({ code, signal }))
    })
    const stdoutDone = pipeOutput(child.stdout, call, stdoutStream, () => void this.children.terminate(child))
    const stderrDone = pipeOutput(child.stderr, call, stderrStream, () => void this.children.terminate(child))
    if (!admissionActive()) {
      await this.children.terminate(child)
      throw rpcError(status.CANCELLED, 'exec was cancelled before process admission completed')
    }

    // Flush and hand over stdin in one synchronous block: no data event can
    // interleave between the queue drain and the switch to direct writes.
    for (const data of queuedInput.splice(0)) {
      child.stdin.write(data)
    }
    onStdinReady()
    if (shouldEndInput()) {
      child.stdin.end()
    }

    const timeoutSeconds = request.timeout_seconds ?? 0
    const effectiveTimeout = timeoutSeconds === 0 ? defaultTimeoutSeconds : timeoutSeconds
    const cancelTimeout = effectiveTimeout > 0
      ? scheduleLongTimeout(effectiveTimeout * 1_000, () => void this.children.terminate(child))
      : undefined

    void Promise.all([closed, stdoutDone, stderrDone]).then(([{ code, signal }]) => {
      cancelTimeout?.()
      if (!call.destroyed && !call.cancelled) {
        const response = {
          stream: exitStream,
          data: Buffer.alloc(0),
          exit_code: resolveExitCode(code, signal),
        }
        call.write(response)
        markTerminalSent()
        call.end()
      }
    })
    return child
  }
}

function assertExecAdmissionActive(
  call: Pick<ServerDuplexStream<ExecInput, ExecOutput>, 'cancelled' | 'destroyed'>,
  admissionActive: () => boolean,
): void {
  if (!admissionActive() || call.cancelled || call.destroyed) {
    throw rpcError(status.CANCELLED, 'exec was cancelled before process admission')
  }
}

function pipeOutput(
  source: Readable,
  call: ServerDuplexStream<ExecInput, ExecOutput>,
  stream: number,
  onFailure: () => void,
): Promise<void> {
  return new Promise(resolve => {
    let finished = false
    const finish = () => {
      if (!finished) {
        finished = true
        resolve()
      }
    }
    source.on('data', (chunk: Buffer | string) => {
      try {
        const response = {
          stream,
          data: Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk),
          exit_code: 0,
        }
        const writable = call.write(response)
        if (!writable) {
          source.pause()
          call.once('drain', () => source.resume())
        }
      } catch {
        onFailure()
      }
    })
    source.once('end', finish)
    source.once('close', finish)
    source.once('error', () => {
      onFailure()
      finish()
    })
  })
}

function resolveExitCode(code: number | null, signal: NodeJS.Signals | null): number {
  if (code !== null) {
    return clampInt32(code)
  }
  if (signal) {
    const number = osConstants.signals[signal]
    if (number > 0) {
      return 128 + number
    }
  }
  return -1
}

function clampInt32(value: number): number {
  return Math.max(-2_147_483_648, Math.min(2_147_483_647, value))
}

function scheduleLongTimeout(milliseconds: number, callback: () => void): () => void {
  let cancelled = false
  let timer: NodeJS.Timeout | undefined
  const schedule = (remaining: number) => {
    const delay = Math.min(remaining, 2_147_483_647)
    timer = setTimeout(() => {
      if (cancelled) {
        return
      }
      if (remaining > delay) {
        schedule(remaining - delay)
      } else {
        callback()
      }
    }, delay)
    timer.unref()
  }
  schedule(milliseconds)
  return () => {
    cancelled = true
    if (timer) {
      clearTimeout(timer)
    }
  }
}
