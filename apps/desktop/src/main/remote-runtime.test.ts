import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  DesktopRemoteRuntimeManager,
  type ManagedRuntimeSession,
  type RuntimeEncryption,
  type RuntimeSessionFactory,
} from './remote-runtime'

const runtimeID = '11111111-1111-4111-8111-111111111111'
const replacementRuntimeID = '22222222-2222-4222-8222-222222222222'
const runtimeKey = `mrk_${'a'.repeat(64)}`
const replacementRuntimeKey = `mrk_${'b'.repeat(64)}`
const runtimeTeamId = '33333333-3333-4333-8333-333333333333'
const temporaryDirectories: string[] = []

afterEach(async () => {
  await Promise.all(temporaryDirectories.splice(0).map(path => rm(path, { recursive: true, force: true })))
})

describe('DesktopRemoteRuntimeManager', () => {
  it('restores an encrypted runtime configuration using main-process-owned connection values', async () => {
    const fixture = await createFixture()
    const first = fixture.manager({ createSession: resolvedSessionFactory() })
    await first.configure({
      runtimeId: runtimeID,
      name: 'Studio Mac',
      key: runtimeKey,
      teamId: runtimeTeamId,
    })

    const restoredFactory = vi.fn<RuntimeSessionFactory>(() => resolvedSession())
    const trustedACPLaunchers = {
      'codex-acp': false,
      'claude-agent-acp': false,
    } as const
    const restored = fixture.manager({
      createSession: restoredFactory,
      trustedACPLaunchers,
    })
    const state = await restored.restore()

    expect(state).toMatchObject({
      enabled: true,
      runtimeId: runtimeID,
      runtimeName: 'Studio Mac',
      status: 'connecting',
      deviceName: 'Test workstation',
    })
    expect(restoredFactory).toHaveBeenCalledWith(
      {
        serverUrl: 'http://localhost:18080/',
        key: runtimeKey,
        teamId: runtimeTeamId,
        workspaceBase: fixture.workspaceBase,
        insecureLocalhost: true,
      },
      expect.objectContaining({
        onStatus: expect.any(Function),
        trustedACPLaunchers,
      }),
    )
  })

  it('uses the device name for a saved configuration created before runtime names were stored', async () => {
    const fixture = await createFixture()
    const encryptedKey = fixture.encryption.encrypt(runtimeKey).toString('base64')
    await writeFile(fixture.configPath, JSON.stringify({
      version: 1,
      runtimeId: runtimeID,
      serverUrl: 'http://localhost:18080/',
      encryptedKey,
    }))

    const state = await fixture.manager({ createSession: resolvedSessionFactory() }).restore()

    expect(state).toMatchObject({
      enabled: true,
      runtimeId: runtimeID,
      runtimeName: 'Test workstation',
      status: 'connecting',
    })
  })

  it('fails closed when a saved team ID is not a string', async () => {
    const fixture = await createFixture()
    const encryptedKey = fixture.encryption.encrypt(runtimeKey).toString('base64')
    await writeFile(fixture.configPath, JSON.stringify({
      version: 1,
      runtimeId: runtimeID,
      serverUrl: 'http://localhost:18080/',
      teamId: 42,
      encryptedKey,
    }))
    const decrypt = vi.spyOn(fixture.encryption, 'decrypt')
    const factory = vi.fn<RuntimeSessionFactory>(() => resolvedSession())

    const state = await fixture.manager({ createSession: factory }).restore()

    expect(state).toMatchObject({
      enabled: true,
      status: 'error',
      error: 'This computer\'s saved connection could not be read',
    })
    expect(decrypt).not.toHaveBeenCalled()
    expect(factory).not.toHaveBeenCalled()
  })

  it('fails closed without decrypting when the saved server does not match the current server', async () => {
    const fixture = await createFixture()
    await fixture.manager({ createSession: resolvedSessionFactory() })
      .configure({ runtimeId: runtimeID, name: 'Studio Mac', key: runtimeKey })

    const decrypt = vi.spyOn(fixture.encryption, 'decrypt')
    const factory = vi.fn<RuntimeSessionFactory>(() => resolvedSession())
    const restored = fixture.manager({
      currentServerUrl: () => 'https://other.example.com',
      createSession: factory,
    })

    await expect(restored.restore()).resolves.toMatchObject({
      enabled: true,
      runtimeId: runtimeID,
      status: 'error',
      error: 'This computer is connected to a different server',
    })
    expect(decrypt).not.toHaveBeenCalled()
    expect(factory).not.toHaveBeenCalled()
  })

  it('waits for the previous run to finish before replacement and for the active run during stop', async () => {
    const fixture = await createFixture()
    const first = deferredSession()
    const second = deferredSession()
    const factory = vi.fn<RuntimeSessionFactory>()
      .mockReturnValueOnce(first.session)
      .mockReturnValueOnce(second.session)
    const manager = fixture.manager({ createSession: factory })

    await manager.configure({ runtimeId: runtimeID, name: 'Studio Mac', key: runtimeKey })
    await vi.waitFor(() => expect(first.session.start).toHaveBeenCalledOnce())

    let replacementSettled = false
    const replacement = manager
      .configure({ runtimeId: replacementRuntimeID, name: 'Office Mac', key: replacementRuntimeKey })
      .finally(() => { replacementSettled = true })
    await vi.waitFor(() => expect(first.session.stop).toHaveBeenCalledOnce())
    await Promise.resolve()
    expect(replacementSettled).toBe(false)
    expect(second.session.start).not.toHaveBeenCalled()

    first.finish()
    await expect(replacement).resolves.toMatchObject({
      runtimeId: replacementRuntimeID,
      runtimeName: 'Office Mac',
      status: 'connecting',
    })
    await vi.waitFor(() => expect(second.session.start).toHaveBeenCalledOnce())

    let stopSettled = false
    const stopping = manager.stop().finally(() => { stopSettled = true })
    await vi.waitFor(() => expect(second.session.stop).toHaveBeenCalledOnce())
    await Promise.resolve()
    expect(stopSettled).toBe(false)

    second.finish()
    await expect(stopping).resolves.toMatchObject({
      enabled: true,
      runtimeId: replacementRuntimeID,
      status: 'stopped',
    })
  })

  it('never exposes or stores the plaintext key outside the managed session', async () => {
    const fixture = await createFixture()
    let reportStatus: ((status: 'disconnected', error?: string) => void) | undefined
    const pending = deferredSession()
    const factory = vi.fn<RuntimeSessionFactory>((_config, options) => {
      reportStatus = options.onStatus
      return pending.session
    })
    const manager = fixture.manager({ createSession: factory })
    const observed: unknown[] = []
    manager.onStateChanged(state => observed.push(state))

    const configured = await manager.configure({
      runtimeId: runtimeID,
      name: 'Studio Mac',
      key: runtimeKey,
      teamId: runtimeTeamId,
    })
    reportStatus?.('disconnected', `credential ${runtimeKey} was rejected`)

    const serializedState = JSON.stringify([configured, manager.runtimeState(), observed])
    const stored = await readFile(fixture.configPath, 'utf8')
    expect(serializedState).not.toContain(runtimeKey)
    expect(serializedState).toContain('[redacted]')
    expect(stored).not.toContain(runtimeKey)
    expect(JSON.parse(stored)).toEqual(expect.objectContaining({
      version: 1,
      runtimeId: runtimeID,
      runtimeName: 'Studio Mac',
      serverUrl: 'http://localhost:18080/',
      teamId: runtimeTeamId,
      encryptedKey: expect.any(String),
    }))

    pending.finish()
    await manager.stop()
  })
})

async function createFixture() {
  const root = await mkdtemp(join(tmpdir(), 'memoh-desktop-runtime-'))
  temporaryDirectories.push(root)
  const configPath = join(root, 'remote-runtime.json')
  const workspaceBase = join(root, 'home')
  const encryption = memoryEncryption()
  return {
    configPath,
    workspaceBase,
    encryption,
    manager(overrides: Partial<ConstructorParameters<typeof DesktopRemoteRuntimeManager>[0]> = {}) {
      return new DesktopRemoteRuntimeManager({
        configPath,
        currentServerUrl: () => 'http://localhost:18080',
        workspaceBase,
        deviceName: 'Test workstation',
        encryption,
        ...overrides,
      })
    },
  }
}

function memoryEncryption(): RuntimeEncryption {
  const values = new Map<string, string>()
  let sequence = 0
  return {
    isAvailable: vi.fn(() => true),
    encrypt: vi.fn((value: string) => {
      const encrypted = `cipher-${++sequence}`
      values.set(encrypted, value)
      return Buffer.from(encrypted)
    }),
    decrypt: vi.fn((value: Buffer) => {
      const decrypted = values.get(value.toString())
      if (!decrypted) throw new Error('unknown ciphertext')
      return decrypted
    }),
  }
}

function resolvedSessionFactory(): RuntimeSessionFactory {
  return () => resolvedSession()
}

function resolvedSession(): ManagedRuntimeSession {
  return {
    start: vi.fn(async () => undefined),
    stop: vi.fn(),
  }
}

function deferredSession() {
  let finish!: () => void
  const run = new Promise<void>((resolve) => { finish = resolve })
  return {
    finish,
    session: {
      start: vi.fn(() => run),
      stop: vi.fn(),
    },
  }
}
