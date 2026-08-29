import { Buffer } from 'node:buffer'
import { chmod, mkdtemp, realpath, rm, writeFile } from 'node:fs/promises'
import { createServer } from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { WebSocketServer } from 'ws'
import { describe, expect, it } from 'vitest'

import {
  assertSecureRuntimeUrl,
  createHandshakeMetadata,
  handshakeHeaders,
  RuntimeSession,
  runtimeConnectUrl,
  runtimeProtocolGrpc,
} from '../src/session'

const runtimeKey = `mrk_${'a'.repeat(64)}`
const runtimeTeamId = '11111111-1111-4111-8111-111111111111'

describe('runtime handshake', () => {
  it('preserves an API base path when building the public WebSocket URL', () => {
    expect(runtimeConnectUrl('https://memoh.example/api').href)
      .toBe('wss://memoh.example/api/runtimes/connect')
    expect(runtimeConnectUrl('http://127.0.0.1:8080/').href)
      .toBe('ws://127.0.0.1:8080/runtimes/connect')
    expect(runtimeConnectUrl('https://memoh.example/prefix/?ignored=1#fragment').href)
      .toBe('wss://memoh.example/prefix/runtimes/connect')
  })

  it('uses the selected protocol and one base64url Unicode metadata header', () => {
    const key = runtimeKey
    const metadata = createHandshakeMetadata('/Users/测试/工作', '1.2.3', {
      hostname: '爱丽丝.local',
      os: 'darwin',
      arch: 'arm64',
    })
    const headers = handshakeHeaders({
      serverUrl: 'https://example.test/api',
      key,
      teamId: runtimeTeamId,
      workspaceBase: metadata.workspace_base,
    }, '1.2.3', metadata)

    expect(metadata.capabilities).toContain('host_fs')
    expect(headers['Sec-WebSocket-Protocol']).toBe(runtimeProtocolGrpc)
    expect(headers.Authorization).toBe(`Bearer ${key}`)
    expect(headers['X-Team-ID']).toBe(runtimeTeamId)
    expect(headers).not.toHaveProperty('X-Memoh-Runtime-Hostname')
    const decoded = JSON.parse(Buffer.from(headers['X-Memoh-Runtime-Metadata'], 'base64url').toString('utf8'))
    expect(decoded).toEqual(metadata)
    expect(Object.keys(decoded).sort()).toEqual([
      'arch',
      'capabilities',
      'client_version',
      'hostname',
      'os',
      'version',
      'workspace_base',
    ])
    expect(headers['X-Memoh-Runtime-Metadata']).not.toContain('=')

    const windowsMetadata = createHandshakeMetadata(String.raw`C:\Users\alice\Memoh`, '1.2.3', {
      hostname: 'alice-pc',
      os: 'win32',
      arch: 'x64',
    })
    expect(windowsMetadata.workspace_base).toBe(String.raw`C:\Users\alice\Memoh`)
  })

  it('accepts detected ACP capabilities without changing legacy metadata calls', () => {
    const machine = {
      hostname: 'alice.local',
      os: 'darwin' as const,
      arch: 'arm64',
    }
    const legacy = createHandshakeMetadata('/Users/alice', '1.2.3', machine)
    expect(legacy.capabilities).toEqual(['fs', 'exec', 'host_fs'])

    const detected = createHandshakeMetadata('/Users/alice', '1.2.3', machine, [
      'fs',
      'exec',
      'host_fs',
      'acp_codex',
      'acp_claude_code',
    ])
    expect(detected.capabilities).toEqual([
      'fs',
      'exec',
      'host_fs',
      'acp_codex',
      'acp_claude_code',
    ])
  })

  it('allows plaintext only for an explicitly enabled loopback target', () => {
    expect(() => assertSecureRuntimeUrl(new URL('ws://127.0.0.1:8080/runtimes/connect'), true)).not.toThrow()
    expect(() => assertSecureRuntimeUrl(new URL('ws://localhost:8080/runtimes/connect'), false)).toThrow()
    expect(() => assertSecureRuntimeUrl(new URL('ws://192.168.1.10:8080/runtimes/connect'), true)).toThrow()
    expect(() => assertSecureRuntimeUrl(new URL('wss://example.test/api/runtimes/connect'))).not.toThrow()
  })

  it('sends the production headers and preserved path in a real WebSocket handshake', async () => {
    const root = await mkdtemp(join(tmpdir(), 'memoh-runtime-session-'))
    const controller = new AbortController()
    const statuses: string[] = []
    let running: Promise<void> | undefined
    const server = new WebSocketServer({
      port: 0,
      handleProtocols: protocols => protocols.has(runtimeProtocolGrpc) ? runtimeProtocolGrpc : false,
    })
    try {
      await new Promise<void>(resolve => server.once('listening', resolve))
      const port = (server.address() as { port: number }).port
      const requestPromise = new Promise<import('node:http').IncomingMessage>(resolve => {
        server.once('connection', (socket, request) => {
          socket.on('error', () => undefined)
          resolve(request)
        })
      })
      const key = runtimeKey
      const adapterEntry = join(root, 'codex-entry.cjs')
      await writeFile(adapterEntry, '// fixed adapter fixture\n')
      const codexExecutable = join(root, 'codex')
      await writeFile(codexExecutable, '#!/bin/sh\nexit 0\n', { mode: 0o700 })
      await chmod(codexExecutable, 0o700)
      const session = new RuntimeSession({
        serverUrl: `http://127.0.0.1:${port}/api`,
        key,
        teamId: runtimeTeamId,
        workspaceBase: root,
        insecureLocalhost: true,
      }, {
        random: () => 0.5,
        onStatus: status => statuses.push(status),
        trustedACPLaunchers: process.platform === 'win32'
          ? undefined
          : {
              'codex-acp': {
                nodeExecutable: process.execPath,
                adapterEntry,
                codexExecutable,
              },
            },
      })
      running = session.start(controller.signal)
      const request = await requestPromise
      expect(request.url).toBe('/api/runtimes/connect')
      expect(request.headers.authorization).toBe(`Bearer ${key}`)
      expect(request.headers['x-team-id']).toBe(runtimeTeamId)
      expect(request.headers['sec-websocket-protocol']).toBe(runtimeProtocolGrpc)
      const encoded = request.headers['x-memoh-runtime-metadata']
      expect(typeof encoded).toBe('string')
      const decoded = JSON.parse(Buffer.from(String(encoded), 'base64url').toString('utf8'))
      expect(decoded).toMatchObject({
        version: 1,
        workspace_base: await realpath(root),
      })
      if (process.platform !== 'win32') {
        expect(decoded.capabilities).toContain('acp_codex')
      }
      expect(JSON.stringify(decoded)).not.toContain(process.execPath)
      expect(JSON.stringify(decoded)).not.toContain(adapterEntry)
      controller.abort()
      await running
      expect(statuses).toContain('connected')
      expect(statuses.at(-1)).toBe('stopped')
    } finally {
      controller.abort()
      await running?.catch(() => undefined)
      for (const client of server.clients) {
        client.terminate()
      }
      await new Promise<void>(resolve => server.close(() => resolve()))
      await rm(root, { recursive: true, force: true })
    }
  })

  it('includes a bounded server response body in handshake errors', async () => {
    const root = await mkdtemp(join(tmpdir(), 'memoh-runtime-rejection-'))
    const controller = new AbortController()
    let running: Promise<void> | undefined
    let rejection: string | undefined
    const server = new WebSocketServer({
      port: 0,
      verifyClient: (_info, done) => done(false, 400, 'X-Team-ID is required'),
    })
    try {
      await new Promise<void>(resolve => server.once('listening', resolve))
      const port = (server.address() as { port: number }).port
      const session = new RuntimeSession({
        serverUrl: `http://127.0.0.1:${port}/api`,
        key: runtimeKey,
        workspaceBase: root,
        insecureLocalhost: true,
      }, {
        random: () => 0.5,
        onStatus: (status, error) => {
          if (status !== 'disconnected' || !error) return
          rejection = error
          controller.abort()
        },
      })

      running = session.start(controller.signal)
      await running

      expect(rejection).toBe('runtime handshake rejected with HTTP 400: X-Team-ID is required')
    } finally {
      controller.abort()
      await running?.catch(() => undefined)
      await new Promise<void>(resolve => server.close(() => resolve()))
      await rm(root, { recursive: true, force: true })
    }
  })

  it('stops waiting when a handshake error body never ends', async () => {
    const root = await mkdtemp(join(tmpdir(), 'memoh-runtime-stalled-rejection-'))
    const controller = new AbortController()
    let running: Promise<void> | undefined
    let rejection: string | undefined
    let responseStartedResolve: (() => void) | undefined
    const responseStarted = new Promise<void>((resolve) => {
      responseStartedResolve = resolve
    })
    const server = createServer((_request, response) => {
      response.writeHead(400, {
        'Content-Length': '100',
        'Content-Type': 'text/plain',
      })
      response.write('partial gateway error')
      responseStartedResolve?.()
    })
    try {
      await new Promise<void>((resolve, reject) => {
        server.once('error', reject)
        server.listen(0, '127.0.0.1', resolve)
      })
      const port = (server.address() as { port: number }).port
      const session = new RuntimeSession({
        serverUrl: `http://127.0.0.1:${port}/api`,
        key: runtimeKey,
        workspaceBase: root,
        insecureLocalhost: true,
      }, {
        random: () => 0.5,
        onStatus: (status, error) => {
          if (status !== 'disconnected' || !error) return
          rejection = error
          controller.abort()
        },
      })

      running = session.start(controller.signal)
      await responseStarted
      await running

      expect(rejection).toBe('runtime handshake rejected with HTTP 400: partial gateway error')
    } finally {
      controller.abort()
      await running?.catch(() => undefined)
      const closed = new Promise<void>(resolve => server.close(() => resolve()))
      server.closeAllConnections()
      await closed
      await rm(root, { recursive: true, force: true })
    }
  }, 10_000)
})
