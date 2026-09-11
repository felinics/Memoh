import { chmod, mkdir, mkdtemp, rm, stat, symlink, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { afterEach, describe, expect, it } from 'vitest'

import {
  normalizeRuntimeEnrollment,
  readRuntimeEnrollment,
  writeRuntimeEnrollment,
} from '../src/runtime-config'

const runtimeKey = `mrk_${'a'.repeat(64)}`
const teamID = '22222222-2222-4222-8222-222222222222'
const temporaryDirectories: string[] = []

afterEach(async () => {
  await Promise.all(temporaryDirectories.splice(0).map(path => rm(path, { recursive: true, force: true })))
})

describe('runtime enrollment configuration', () => {
  it('writes a versioned private configuration and reads its normalized values', async () => {
    const root = await temporaryDirectory()
    const configPath = join(root, 'private', 'config.json')
    const enrollment = normalizeRuntimeEnrollment({
      serverUrl: 'wss://memoh.example/api///',
      key: ` ${runtimeKey} `,
      teamId: teamID.toUpperCase(),
    }, root)

    await writeRuntimeEnrollment(configPath, enrollment)

    await expect(readRuntimeEnrollment(configPath, root)).resolves.toEqual({
      schemaVersion: 1,
      serverUrl: 'https://memoh.example/api',
      key: runtimeKey,
      teamId: teamID,
      insecureLocalhost: false,
    })
    if (process.platform !== 'win32') {
      expect((await stat(configPath)).mode & 0o777).toBe(0o600)
      expect((await stat(join(root, 'private'))).mode & 0o777).toBe(0o700)
    }
  })

  it.runIf(process.platform !== 'win32')('keeps the key private inside a parent directory shared with other components', async () => {
    const root = await temporaryDirectory()
    const shared = join(root, '.memoh')
    await mkdir(shared)
    await chmod(shared, 0o750)
    const configPath = join(shared, 'runtime.json')

    await writeRuntimeEnrollment(configPath, normalizeRuntimeEnrollment({
      serverUrl: 'wss://memoh.example/api',
      key: runtimeKey,
    }))

    expect((await stat(configPath)).mode & 0o777).toBe(0o600)
    expect((await stat(shared)).mode & 0o777).toBe(0o750)
  })

  it('rejects malformed and unsupported configurations without echoing the key', async () => {
    const root = await temporaryDirectory()
    const configPath = join(root, 'config.json')
    await writeFile(configPath, JSON.stringify({ schemaVersion: 2, serverUrl: 'https://example.com', key: runtimeKey }), { mode: 0o600 })

    await expect(readRuntimeEnrollment(configPath, root)).rejects.toThrow('unsupported schema version')
    await expect(readRuntimeEnrollment(configPath, root)).rejects.not.toThrow(runtimeKey)
    expect(() => normalizeRuntimeEnrollment({
      serverUrl: 'https://memoh.example/api?credential=unexpected',
      key: runtimeKey,
    }, root)).toThrow('query string or fragment')
  })

  it.runIf(process.platform !== 'win32')('refuses to read a credential through a symbolic link', async () => {
    const root = await temporaryDirectory()
    const target = join(root, 'actual.json')
    const link = join(root, 'runtime.json')
    await writeFile(target, JSON.stringify(normalizeRuntimeEnrollment({
      serverUrl: 'https://memoh.example',
      key: runtimeKey,
    }, root)))
    await symlink(target, link)

    await expect(readRuntimeEnrollment(link, root)).rejects.toThrow('could not be read safely')
  })

})

async function temporaryDirectory(): Promise<string> {
  const path = await mkdtemp(join(tmpdir(), 'memoh-runtime-config-'))
  temporaryDirectories.push(path)
  return path
}

it.runIf(process.platform !== 'win32')('rejects a writable ancestor even when the immediate parent is private', async () => {
  const root = await temporaryDirectory()
  const shared = join(root, 'shared')
  const privateDirectory = join(shared, 'private')
  await mkdir(privateDirectory, { recursive: true, mode: 0o700 })
  await chmod(shared, 0o777)
  await expect(writeRuntimeEnrollment(join(privateDirectory, 'config.json'), normalizeRuntimeEnrollment({ serverUrl: 'https://one.example', key: runtimeKey }))).rejects.toThrow('not writable')
})

it.runIf(process.platform !== 'win32')('rejects a symlinked ancestor', async () => {
  const root = await temporaryDirectory()
  await mkdir(join(root, 'target'))
  await symlink(join(root, 'target'), join(root, 'link'))
  await expect(writeRuntimeEnrollment(join(root, 'link', 'nested', 'config.json'), normalizeRuntimeEnrollment({ serverUrl: 'https://one.example', key: runtimeKey }))).rejects.toThrow('unsafe runtime directory')
})
