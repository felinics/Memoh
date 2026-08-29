import { EventEmitter } from 'node:events'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { afterAll, beforeAll, describe, expect, it } from 'vitest'

import type { ChildProcessWithoutNullStreams } from 'node:child_process'

import { WorkspaceExecService } from '../src/core/exec'
import type { ExecOutput } from '../src/types'

class FakeExecCall extends EventEmitter {
  cancelled = false
  destroyed = false
  ended = false
  frames: ExecOutput[] = []

  write(frame: ExecOutput): boolean {
    this.frames.push(frame)
    return true
  }

  end(): void {
    this.ended = true
  }

  stdout(): string {
    return Buffer.concat(this.frames.filter(frame => frame.stream === 0).map(frame => Buffer.from(frame.data))).toString()
  }
}

async function waitFor(condition: () => boolean, timeoutMs = 5_000): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (!condition()) {
    if (Date.now() > deadline) {
      throw new Error('waitFor timed out')
    }
    await new Promise(resolve => setTimeout(resolve, 5))
  }
}

describe.skipIf(process.platform === 'win32')('WorkspaceExecService stdin ordering', () => {
  let root = ''
  let script = ''

  beforeAll(async () => {
    root = await mkdtemp(join(tmpdir(), 'memoh-exec-order-'))
    script = join(root, 'stdin-echo.cjs')
    await writeFile(script, `
let input = ''
process.stdin.setEncoding('utf8')
process.stdin.on('data', chunk => { input += chunk })
process.stdin.on('end', () => process.stdout.write(input))
`)
  })

  afterAll(async () => {
    await rm(root, { recursive: true, force: true })
  })

  it('keeps frames arriving in the spawn window behind queued first-frame bytes', async () => {
    // The dangerous window is after spawn() has set `child` but before
    // start() flushes the queued first-frame stdin bytes. A controllable
    // register() promise holds start() inside exactly that window.
    let releaseRegistration: (() => void) | undefined
    const children = {
      register: (_child: ChildProcessWithoutNullStreams) =>
        new Promise<void>(resolve => { releaseRegistration = resolve }),
      terminate: async (child: ChildProcessWithoutNullStreams) => {
        child.kill('SIGKILL')
      },
    }
    const paths = {
      defaultDirectory: root,
      resolve: async (path: string) => path,
      revalidate: async (path: string) => path,
    }
    const service = new WorkspaceExecService(paths, children)
    const call = new FakeExecCall()
    service.exec(call as never)

    call.emit('data', {
      command: `node ${JSON.stringify(script)}`,
      stdin_data: Buffer.from('one'),
    })
    await waitFor(() => releaseRegistration !== undefined)

    // These frames land while start() awaits registration: the child exists,
    // but the first frame's bytes are still queued.
    call.emit('data', { stdin_data: Buffer.from('-two') })
    call.emit('data', { stdin_data: Buffer.from('-three') })
    call.emit('end')
    releaseRegistration?.()

    await waitFor(() => call.ended)
    expect(call.stdout()).toBe('one-two-three')
    expect(call.frames.at(-1)).toMatchObject({ stream: 2, exit_code: 0 })
  })
})
