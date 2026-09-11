import { execFile } from 'node:child_process'
import * as childProcess from 'node:child_process'
import * as fs from 'node:fs/promises'
import { mkdtemp, readdir, realpath, rm, writeFile } from 'node:fs/promises'
import { tmpdir, userInfo } from 'node:os'
import { dirname, join } from 'node:path'
import { promisify } from 'node:util'

import { afterEach, describe, expect, it, vi } from 'vitest'

import { checkDirectory, readPrivateFile, writeFileAtomic } from '../src/secure-files'
import { protectWindowsDirectory, protectWindowsFile } from '../src/windows-file-security'

vi.mock('node:fs/promises', async importOriginal => ({
  ...await importOriginal<typeof fs>(),
}))
vi.mock('node:child_process', async importOriginal => ({
  ...await importOriginal<typeof childProcess>(),
}))

const directories: string[] = []

afterEach(async () => {
  vi.restoreAllMocks()
  vi.unstubAllEnvs()
  await Promise.all(directories.splice(0).map(path => rm(path, { recursive: true, force: true })))
})

describe('atomic credential writes', () => {
  it.runIf(process.platform === 'win32')('reapplies private ACLs to existing files and directories', async () => {
    // Simulate Node inheriting module paths that cannot load in Windows PowerShell.
    vi.stubEnv('PSModulePath', join(tmpdir(), 'memoh-no-powershell-modules'))
    const directory = await mkdtemp(join(tmpdir(), 'memoh-acl-repeat-'))
    directories.push(directory)
    const file = join(directory, 'credential')
    await protectWindowsDirectory(directory)
    await protectWindowsDirectory(directory)
    await writeFile(file, 'test credential')
    await protectWindowsFile(file)
    await protectWindowsFile(file)
    expect(await readPrivateFile(file)).toBe('test credential')
  }, 120_000)

  it.runIf(process.platform === 'darwin')('creates the file without inherited read access before writing any bytes', async () => {
    const path = await credentialFile()
    const parent = dirname(path)
    const command = promisify(execFile)
    await command('/bin/chmod', ['+a', 'everyone allow read,execute,file_inherit,directory_inherit', parent])
    const aclLines = async (target: string) => (await command('/bin/ls', ['-lde', target])).stdout
      .split('\n').filter(line => /^\s*\d+:/.test(line))
    const parentACL = await aclLines(parent)
    expect(parentACL.length).toBeGreaterThan(0)
    const originalOpen = (await vi.importActual<typeof fs>('node:fs/promises')).open
    let privateCreationObserved = false
    vi.spyOn(fs, 'open').mockImplementation(async (...args) => {
      const handle = await originalOpen(...args)
      if (args[1] === 'wx') {
        try {
          // Inspect at the first open, before per-file ACL changes or writes.
          expect(await aclLines(String(args[0]))).toEqual([])
          expect((await handle.stat()).mode & 0o077).toBe(0)
          privateCreationObserved = true
        } catch (error) {
          await handle.close()
          throw error
        }
      }
      return handle
    })
    await writeFileAtomic(path, 'private enrollment', 0o600)
    expect(privateCreationObserved).toBe(true)
    expect(await readPrivateFile(path)).toBe('private enrollment')
    expect(await aclLines(parent)).toEqual(parentACL)
    expect(await readdir(parent)).toEqual(['credential'])
  })

  it('preserves the committed destination and removes staging after rename fails', async () => {
    const path = await credentialFile()
    const parent = dirname(path)
    const destination = join(parent, 'existing-directory')
    await fs.mkdir(destination)
    await writeFile(join(destination, 'keep'), 'old state')
    await expect(writeFileAtomic(destination, 'new state', 0o600)).rejects.toThrow()
    expect(await fs.readFile(join(destination, 'keep'), 'utf8')).toBe('old state')
    expect((await readdir(parent)).sort()).toEqual(['credential', 'existing-directory'])
  })
})

describe.runIf(process.platform === 'darwin')('inherited macOS ACLs', () => {
  const command = promisify(execFile)

  it('rejects a 0600 credential with inherited read access for other accounts', async () => {
    const parent = dirname(await credentialFile())
    await command('/bin/chmod', ['+a', 'everyone allow read,execute,file_inherit,directory_inherit', parent])
    const path = join(parent, 'inherited')
    await writeFile(path, 'private credential', { mode: 0o600 })
    expect((await fs.stat(path)).mode & 0o777).toBe(0o600)
    const acl = (await command('/bin/ls', ['-lde', path])).stdout
    expect(acl).toContain('inherited allow read')
    await expect(readPrivateFile(path)).rejects.toThrow('unsafe extended ACL')
    expect((await command('/bin/ls', ['-lde', path])).stdout).toBe(acl)
  })

  it('rejects inherited directory write access after the parent ACL is removed', async () => {
    const parent = dirname(await credentialFile())
    await command('/bin/chmod', ['+a', 'everyone allow write,delete,delete_child,file_inherit,directory_inherit,only_inherit', parent])
    const child = join(parent, 'child')
    await fs.mkdir(child, { mode: 0o700 })
    await command('/bin/chmod', ['-N', parent])
    const acl = (await command('/bin/ls', ['-lde', child])).stdout
    expect(acl).toContain('inherited allow add_file,delete,delete_child')
    await expect(checkDirectory(child)).rejects.toThrow('writable extended ACL')
    expect((await command('/bin/ls', ['-lde', child])).stdout).toBe(acl)
  })

  it('allows explicit and inherited ACLs granted only to the current user', async () => {
    const parent = dirname(await credentialFile())
    await fs.chmod(parent, 0o755)
    await command('/bin/chmod', ['+a', `user:${userInfo().username} allow read,write,delete,file_inherit,directory_inherit`, parent])
    const path = join(parent, 'self')
    await writeFile(path, 'private credential', { mode: 0o600 })
    expect((await command('/bin/ls', ['-lde', path])).stdout).toContain('inherited allow')
    await expect(checkDirectory(parent)).resolves.toBeUndefined()
    await expect(readPrivateFile(path)).resolves.toBe('private credential')
  })

  it('rejects unrecognized ACL entries instead of silently treating them as safe', async () => {
    const parent = dirname(await credentialFile())
    const mock = vi.spyOn(childProcess, 'execFile')
    Object.defineProperty(mock, promisify.custom, {
      value: async () => ({ stdout: ' 0: unsupported ACL entry\n', stderr: '' }),
      configurable: true,
    })
    await expect(checkDirectory(parent)).rejects.toThrow('could not verify macOS ACL entry')
  })
})

async function credentialFile(): Promise<string> {
  const directory = await mkdtemp(join(await realpath(tmpdir()), 'memoh-credential-test-'))
  directories.push(directory)
  const path = join(directory, 'credential')
  await writeFile(path, 'old credential', { mode: 0o600 })
  return path
}
