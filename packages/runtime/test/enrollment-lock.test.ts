import { execFile } from 'node:child_process'
import { mkdir, mkdtemp, readFile, rm, stat } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'
import { build } from 'esbuild'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import { withEnrollmentLock } from '../src/enrollment-lock'
import { readRuntimeEnrollment, writeRuntimeEnrollment, normalizeRuntimeEnrollment } from '../src/runtime-config'

const roots: string[] = []
const exec = promisify(execFile)
let buildRoot: string
let cli: string

beforeAll(async () => {
  buildRoot = await mkdtemp(join(tmpdir(), 'memoh-enroll-cli-'))
  cli = join(buildRoot, 'cli.mjs')
  await build({
    entryPoints: [fileURLToPath(new URL('../src/cli.ts', import.meta.url))],
    bundle: true, platform: 'node', format: 'esm', target: 'node20', outfile: cli,
    banner: { js: 'import { createRequire } from \'node:module\'; const require = createRequire(import.meta.url);' },
  })
})
afterAll(async () => { await rm(buildRoot, { recursive: true, force: true }) })
afterEach(async () => { await Promise.all(roots.splice(0).map(path => rm(path, { recursive: true, force: true }))) })

describe('enrollment locking across processes', () => {
  it('allows only one concurrent initial enrollment and permits explicit replacement afterward', async () => {
    const { home, config } = await fixture()
    const outcomes = await Promise.allSettled(['a', 'b', 'c'].map(letter => enroll(home, letter)))
    expect(outcomes.filter(result => result.status === 'fulfilled')).toHaveLength(1)
    for (const result of outcomes) {
      if (result.status === 'rejected') expect(String(result.reason)).toContain('--replace')
    }
    const winner = ['a', 'b', 'c'][outcomes.findIndex(result => result.status === 'fulfilled')]
    expect((await readRuntimeEnrollment(config)).serverUrl).toBe(`https://${winner}.example/`)
    await expect(stat(`${config}.lock`)).rejects.toMatchObject({ code: 'ENOENT' })
    await enroll(home, winner)
    await enroll(home, 'd', true)
    expect((await readRuntimeEnrollment(config)).serverUrl).toBe('https://d.example/')
  }, 120_000)

  it('rereads the configuration after acquiring the lock and preserves enrollment written by its previous owner', async () => {
    const { home, config } = await fixture()
    let contender: ReturnType<typeof enroll>
    const enrollment = normalizeRuntimeEnrollment({ serverUrl: 'https://a.example', key: `mrk_${'a'.repeat(64)}` })
    await withEnrollmentLock(config, async () => {
      contender = enroll(home, 'b')
      // Handle early child-process failures to prevent an unhandled rejection.
      void contender.catch(() => undefined)
      await writeRuntimeEnrollment(config, enrollment)
    })
    await expect(contender!).rejects.toThrow('--replace')
    expect(await readRuntimeEnrollment(config)).toEqual(enrollment)
    await expect(stat(`${config}.lock`)).rejects.toMatchObject({ code: 'ENOENT' })
  }, 120_000)

  it('releases the lock when the callback fails without changing the saved configuration', async () => {
    const { config } = await fixture()
    const enrollment = normalizeRuntimeEnrollment({ serverUrl: 'https://a.example', key: `mrk_${'a'.repeat(64)}` })
    await writeRuntimeEnrollment(config, enrollment)
    await expect(withEnrollmentLock(config, async () => { throw new Error('write failed') })).rejects.toThrow('write failed')
    await expect(stat(`${config}.lock`)).rejects.toMatchObject({ code: 'ENOENT' })
    expect(await readRuntimeEnrollment(config)).toEqual(enrollment)
    await expect(withEnrollmentLock(config, async () => 'retry')).resolves.toBe('retry')
  })

  it('preserves the existing lock and owner record when waiting times out', async () => {
    const { config } = await fixture()
    await withEnrollmentLock(config, async () => {
      const owner = await readFile(join(`${config}.lock`, 'owner.json'), 'utf8')
      expect(JSON.parse(owner)).toEqual({ pid: process.pid })
      await expect(withEnrollmentLock(config, async () => { throw new Error('must not run') }, 0)).rejects.toThrow('confirming that process has exited')
      expect(await readFile(join(`${config}.lock`, 'owner.json'), 'utf8')).toBe(owner)
    })
  })
})

async function fixture() {
  const home = await mkdtemp(join(tmpdir(), 'memoh-enroll-home-'))
  roots.push(home)
  await mkdir(join(home, '.memoh'), { mode: 0o700 })
  return { home, config: join(home, '.memoh', 'runtime.json') }
}

function enroll(home: string, letter: string, replace = false) {
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('MEMOH_RUNTIME_')))
  return exec(process.execPath, [cli, 'enroll', '--server', `https://${letter}.example`, '--key', `mrk_${letter.repeat(64)}`, ...(replace ? ['--replace'] : [])], {
    env: { ...env, HOME: home, USERPROFILE: home }, timeout: 90_000,
  })
}
