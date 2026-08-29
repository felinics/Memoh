import { chmod, mkdtemp, readFile, realpath, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  Client,
  Metadata,
  credentials,
  loadPackageDefinition,
  status,
  type ClientDuplexStream,
  type ClientReadableStream,
  type ClientWritableStream,
  type ServiceError,
} from '@grpc/grpc-js'
import { loadSync } from '@grpc/proto-loader'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { grpcMessageLimit, startRuntimeGrpcServer, type RunningRuntimeGrpcServer } from '../src/service'
import type { ExecInput, ExecOutput, FileEntry, WriteRawChunk } from '../src/types'
import { createGrpcWebSocketTestHarness, type GrpcWebSocketTestHarness } from './grpc-websocket-harness'

interface TestClient extends Client {
  Stat(request: { path: string }, callback: UnaryCallback<{ entry: FileEntry }>): void
  Stat(request: { path: string }, metadata: Metadata, callback: UnaryCallback<{ entry: FileEntry }>): void
  ReadFile(request: { path: string, line_offset?: number, n_lines?: number }, callback: UnaryCallback<{
    content: string
    total_lines: number
    binary: boolean
  }>): void
  ReadFile(request: { path: string, line_offset?: number, n_lines?: number }, metadata: Metadata, callback: UnaryCallback<{
    content: string
    total_lines: number
    binary: boolean
  }>): void
  WriteFile(request: { path: string, content: Buffer }, callback: UnaryCallback<Record<string, never>>): void
  WriteFile(request: { path: string, content: Buffer }, metadata: Metadata, callback: UnaryCallback<Record<string, never>>): void
  ListDir(request: { path: string, recursive?: boolean }, callback: UnaryCallback<{
    entries: FileEntry[]
    total_count: number
    truncated: boolean
  }>): void
  ListDir(request: { path: string, recursive?: boolean }, metadata: Metadata, callback: UnaryCallback<{
    entries: FileEntry[]
    total_count: number
    truncated: boolean
  }>): void
  DeleteFile(request: { path: string, recursive?: boolean }, callback: UnaryCallback<Record<string, never>>): void
  DeleteFile(request: { path: string, recursive?: boolean }, metadata: Metadata, callback: UnaryCallback<Record<string, never>>): void
  ReadRaw(request: { path: string }, metadata: Metadata): ClientReadableStream<{ data: Buffer }>
  WriteRaw(metadata: Metadata, callback: UnaryCallback<{ bytes_written: string }>): ClientWritableStream<WriteRawChunk>
  Exec(metadata: Metadata): ClientDuplexStream<ExecInput, ExecOutput>
}

type UnaryCallback<Response> = (error: ServiceError | null, response: Response) => void
type TestClientConstructor = new (
  address: string,
  channelCredentials: ReturnType<typeof credentials.createInsecure>,
  options: Record<string, number>,
) => TestClient

let root: string
let running: RunningRuntimeGrpcServer
let transport: GrpcWebSocketTestHarness
let client: TestClient

beforeEach(async () => {
  root = await mkdtemp(join(tmpdir(), 'memoh-runtime-service-'))
  running = await startRuntimeGrpcServer({
    workspaceBase: root,
    warn: () => undefined,
  })
  transport = await createGrpcWebSocketTestHarness(running)
  const ClientConstructor = loadTestClientConstructor()
  client = new ClientConstructor(
    transport.target,
    credentials.createInsecure(),
    {
      'grpc.max_receive_message_length': grpcMessageLimit,
      'grpc.max_send_message_length': grpcMessageLimit,
    },
  )
})

afterEach(async () => {
  client.close()
  await transport.close()
  await running.close()
  await rm(root, { recursive: true, force: true })
})

describe('ContainerService', () => {
  it('passes readiness and performs file RPCs under /data', async () => {
    const readiness = await unaryUnscoped<{ path: string }, { entry: FileEntry }>(client.Stat.bind(client), { path: '/' })
    expect(readiness.entry.is_dir).toBe(true)

    await unary(client.WriteFile.bind(client), { path: '/data/notes/hello.txt', content: Buffer.from('one\ntwo\n') })
    const result = await unary(client.ReadFile.bind(client), { path: '/data/notes/hello.txt', line_offset: 2, n_lines: 1 })
    expect(result).toEqual({ content: 'two\n', total_lines: 2, binary: false })

    const listing = await unary(client.ListDir.bind(client), { path: '/data', recursive: true })
    expect(listing.entries.map(entry => entry.path)).toContain('notes/hello.txt')

    await expect(unary(client.ReadFile.bind(client), { path: '/data/missing.txt' }))
      .rejects.toMatchObject({ code: status.NOT_FOUND })
    await expect(unary(client.DeleteFile.bind(client), { path: '/data/already-missing.txt' })).resolves.toEqual({})
  })

  it('streams raw files in 64 KiB chunks without changing bytes', async () => {
    const content = Buffer.alloc(150_000, 0x5a)
    const writer = client.WriteRaw(new Metadata(), (error, response) => {
      if (error) {
        return
      }
      expect(response.bytes_written).toBe(String(content.length))
    })
    writer.write({ path: '/data/raw.bin', data: content.subarray(0, 70_000) })
    writer.write({ path: '', data: content.subarray(70_000) })
    writer.end()
    await new Promise<void>((resolve, reject) => {
      writer.once('status', statusObject => statusObject.code === status.OK ? resolve() : reject(statusObject))
      writer.once('error', reject)
    })

    const chunks: Buffer[] = []
    const reader = client.ReadRaw({ path: '/data/raw.bin' }, new Metadata())
    reader.on('data', chunk => chunks.push(Buffer.from(chunk.data)))
    await streamEnd(reader)
    expect(Buffer.concat(chunks)).toEqual(content)
    expect(chunks.every(chunk => chunk.length <= 64 * 1024)).toBe(true)
  })

  it('runs host shell commands, streams output, and rejects dangerous env', async () => {
    const outputScript = await writeNodeFixture('output.cjs', `
process.stdout.write('out')
process.stderr.write('err')
process.exitCode = 7
`)
    const output = await exec({ command: nodeScriptCommand(outputScript), work_dir: '/data' })
    expect(Buffer.concat(output.filter(frame => frame.stream === 0).map(frame => frame.data)).toString()).toBe('out')
    expect(Buffer.concat(output.filter(frame => frame.stream === 1).map(frame => frame.data)).toString()).toBe('err')
    expect(output.at(-1)).toMatchObject({ stream: 2, exit_code: 7 })

    await expect(exec({ command: 'echo ok', work_dir: '/data', env: ['PATH=/tmp'] }))
      .rejects.toMatchObject({ code: status.PERMISSION_DENIED })
    await expect(exec({ command: 'echo ok', work_dir: '/data', env: ['ELECTRON_RUN_AS_NODE=1'] }))
      .rejects.toMatchObject({ code: status.PERMISSION_DENIED })
    await expect(exec({ command: 'echo ok', work_dir: '/data', env: ['CODEX_PATH=/tmp/codex'] }))
      .rejects.toMatchObject({ code: status.PERMISSION_DENIED })
    await expect(exec({ command: 'echo ok', work_dir: '/data', env: ['CLAUDE_CODE_EXECUTABLE=/tmp/claude'] }))
      .rejects.toMatchObject({ code: status.PERMISSION_DENIED })
    await expect(exec({ command: 'echo ok', work_dir: '/data', pty: true }))
      .rejects.toMatchObject({ code: status.UNIMPLEMENTED })

    const timeoutScript = await writeNodeFixture('timeout.cjs', 'setInterval(() => {}, 1_000)\n')
    const timedOut = await exec({ command: nodeScriptCommand(timeoutScript), work_dir: '/data', timeout_seconds: 1 })
    expect(timedOut.at(-1)).toMatchObject({ stream: 2 })
    expect(timedOut.at(-1)?.exit_code).not.toBe(0)
    if (process.platform !== 'win32') {
      expect(timedOut.at(-1)?.exit_code).toBe(137)
    }
  })

  it('keeps concurrent bidirectional Exec streams independent', async () => {
    const echoScript = await writeNodeFixture('stream-echo.cjs', `
const [label, delay] = process.argv.slice(2)
let input = ''
process.stdin.setEncoding('utf8')
process.stdin.on('data', chunk => { input += chunk })
process.stdin.on('end', () => {
  setTimeout(() => process.stdout.write(label + ':' + input), Number(delay))
})
`)

    const [first, second] = await Promise.all([
      execWithStdin({
        command: nodeScriptCommand(echoScript, 'first', '30'),
        work_dir: '/data',
      }, ['alpha', '-one']),
      execWithStdin({
        command: nodeScriptCommand(echoScript, 'second', '0'),
        work_dir: '/data',
      }, ['beta', '-two']),
    ])

    expect(stdout(first)).toBe('first:alpha-one')
    expect(stdout(second)).toBe('second:beta-two')
    expect(first.at(-1)).toMatchObject({ stream: 2, exit_code: 0 })
    expect(second.at(-1)).toMatchObject({ stream: 2, exit_code: 0 })
  })

  it('launches a fixed ACP shim through the Runtime-owned PATH', async () => {
    if (process.platform === 'win32') return
    client.close()
    await transport.close()
    await running.close()

    const entry = await writeNodeFixture('trusted-codex-entry.cjs', `
process.stdout.write(JSON.stringify({
  argv: process.argv.slice(2),
  electronRunAsNode: process.env.ELECTRON_RUN_AS_NODE,
  codexPath: process.env.CODEX_PATH,
  claudePath: process.env.CLAUDE_CODE_EXECUTABLE,
}))
`)
    const codex = join(root, 'trusted-codex')
    await writeFile(codex, '#!/bin/sh\nexit 0\n', { mode: 0o700 })
    await chmod(codex, 0o700)
    running = await startRuntimeGrpcServer({
      workspaceBase: root,
      warn: () => undefined,
      trustedACPLaunchers: {
        'codex-acp': {
          nodeExecutable: process.execPath,
          adapterEntry: entry,
          codexExecutable: codex,
        },
      },
    })
    transport = await createGrpcWebSocketTestHarness(running)
    const ClientConstructor = loadTestClientConstructor()
    client = new ClientConstructor(
      transport.target,
      credentials.createInsecure(),
      {
        'grpc.max_receive_message_length': grpcMessageLimit,
        'grpc.max_send_message_length': grpcMessageLimit,
      },
    )

    expect(running.capabilities).toContain('acp_codex')
    const output = await exec({
      command: 'command -v codex-acp && codex-acp \'argument from server\'',
      work_dir: '/data',
      clean_env: true,
      unset_env: ['PATH'],
    })
    const lines = stdout(output).trim().split('\n')
    expect(lines[0]).toMatch(/memoh-runtime-acp-.+\/codex-acp$/)
    expect(lines[0]).not.toContain(process.execPath)
    expect(lines[0]).not.toContain(entry)
    expect(JSON.parse(lines[1])).toEqual({
      argv: ['argument from server'],
      codexPath: codex,
    })
  })

  it('terminates connection-owned process trees on server close', async () => {
    const pidPath = join(root, 'pid')
    const childScript = await writeNodeFixture('child.cjs', 'setInterval(() => {}, 1_000)\n')
    const parentScript = await writeNodeFixture('parent.cjs', `
const { spawn } = require('node:child_process')
const { writeFileSync } = require('node:fs')

const [pidPath, childPath] = process.argv.slice(2)
const child = spawn(process.execPath, [childPath], { stdio: 'ignore', windowsHide: true })
writeFileSync(pidPath, String(child.pid))
setInterval(() => {}, 1_000)
`)
    const stream = client.Exec(new Metadata())
    stream.on('error', () => undefined)
    stream.write({
      command: nodeScriptCommand(parentScript, pidPath, childScript),
      work_dir: '/data',
      timeout_seconds: -1,
    })
    await waitUntil(async () => {
      try {
        return Number(await readFile(pidPath, 'utf8')) > 0
      } catch {
        return false
      }
    })
    const pid = Number(await readFile(pidPath, 'utf8'))
    await running.close()
    await waitUntil(async () => !processExists(pid), 10_000)
    expect(processExists(pid)).toBe(false)
    stream.cancel()
  })


  it('uses the home directory by default and allows host paths outside it', async () => {
    const cwdScript = await writeNodeFixture('cwd.cjs', 'process.stdout.write(process.cwd())\n')
    const defaultExec = await exec({ command: nodeScriptCommand(cwdScript) })
    expect(normalizePath(stdout(defaultExec))).toBe(normalizePath(await realpath(root)))

    const outside = await mkdtemp(join(tmpdir(), 'memoh-runtime-service-outside-'))
    try {
      const outsideFile = join(outside, 'outside.txt')
      await unary(client.WriteFile.bind(client), {
        path: outsideFile,
        content: Buffer.from('outside home'),
      })
      const result = await unary(client.ReadFile.bind(client), { path: outsideFile })
      expect(result.content).toBe('outside home\n')

      const outsideExec = await exec({ command: nodeScriptCommand(cwdScript), work_dir: outside })
      expect(normalizePath(stdout(outsideExec))).toBe(normalizePath(await realpath(outside)))
    } finally {
      await rm(outside, { recursive: true, force: true })
    }
  })

  it('accepts an empty WriteRaw stream without workspace metadata', async () => {
    await expect(emptyWriteRaw(new Metadata())).resolves.toEqual({ bytes_written: '0' })
  })

})

function loadTestClientConstructor(): TestClientConstructor {
  const proto = fileURLToPath(new URL('../../../internal/workspace/bridgepb/bridge.proto', import.meta.url))
  const definition = loadSync(proto, {
    keepCase: true,
    longs: String,
    enums: Number,
    defaults: true,
    oneofs: true,
  })
  const descriptor = loadPackageDefinition(definition) as unknown as {
    bridgepb: { ContainerService: TestClientConstructor }
  }
  return descriptor.bridgepb.ContainerService
}

function unary<Request, Response>(
  method: (request: Request, metadata: Metadata, callback: UnaryCallback<Response>) => void,
  request: Request,
  metadata = new Metadata(),
): Promise<Response> {
  return new Promise((resolve, reject) => {
    method(request, metadata, (error, response) => error ? reject(error) : resolve(response))
  })
}

function unaryUnscoped<Request, Response>(
  method: (request: Request, callback: UnaryCallback<Response>) => void,
  request: Request,
): Promise<Response> {
  return new Promise((resolve, reject) => {
    method(request, (error, response) => error ? reject(error) : resolve(response))
  })
}

function streamEnd(stream: ClientReadableStream<unknown>): Promise<void> {
  return new Promise((resolve, reject) => {
    stream.once('end', resolve)
    stream.once('error', reject)
  })
}

function exec(first: ExecInput, metadata = new Metadata()): Promise<ExecOutput[]> {
  const stream = client.Exec(metadata)
  const frames: ExecOutput[] = []
  stream.on('data', frame => frames.push(frame))
  stream.write(first)
  stream.end()
  return new Promise((resolve, reject) => {
    stream.once('end', () => resolve(frames))
    stream.once('error', reject)
  })
}

function execWithStdin(first: ExecInput, chunks: string[]): Promise<ExecOutput[]> {
  const stream = client.Exec(new Metadata())
  const frames: ExecOutput[] = []
  stream.on('data', frame => frames.push(frame))
  stream.write(first)
  for (const chunk of chunks) {
    stream.write({ stdin_data: Buffer.from(chunk) })
  }
  stream.end()
  return new Promise((resolve, reject) => {
    stream.once('end', () => resolve(frames))
    stream.once('error', reject)
  })
}

function emptyWriteRaw(metadata: Metadata): Promise<{ bytes_written: string }> {
  return new Promise((resolve, reject) => {
    const writer = client.WriteRaw(metadata, (error, response) => error ? reject(error) : resolve(response))
    // Callback-style grpc-js streams also emit their terminal error. The
    // callback owns the assertion, so keep the EventEmitter error handled.
    writer.on('error', () => undefined)
    writer.end()
  })
}

function stdout(frames: ExecOutput[]): string {
  return Buffer.concat(frames.filter(frame => frame.stream === 0).map(frame => frame.data)).toString()
}

async function writeNodeFixture(name: string, source: string): Promise<string> {
  const path = join(root, name)
  await writeFile(path, source)
  return path
}

function nodeScriptCommand(scriptPath: string, ...args: string[]): string {
  return `node ${[scriptPath, ...args].map(quoteShellArgument).join(' ')}`
}

function quoteShellArgument(value: string): string {
  if (process.platform === 'win32') {
    if (value.includes('"')) {
      throw new Error('Windows test paths must not contain quotes')
    }
    return `"${value}"`
  }
  const escaped = value.replaceAll('\u0027', '\u0027"\u0027"\u0027')
  return `'${escaped}'`
}

function normalizePath(value: string): string {
  const path = value.trim()
  return process.platform === 'win32' ? path.toLowerCase() : path
}

async function waitUntil(predicate: () => Promise<boolean>, timeout = 5_000): Promise<void> {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    if (await predicate()) {
      return
    }
    await new Promise(resolve => setTimeout(resolve, 25))
  }
  throw new Error('condition did not become true')
}

function processExists(pid: number): boolean {
  try {
    process.kill(pid, 0)
    return true
  } catch {
    return false
  }
}
