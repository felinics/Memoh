import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { randomUUID } from 'node:crypto'
import { constants } from 'node:fs'
import { chmod, lstat, mkdir, open, realpath, rename, rm } from 'node:fs/promises'
import { userInfo } from 'node:os'
import { dirname, join, parse, resolve } from 'node:path'
import { checkWindowsAccess, protectWindowsDirectory, protectWindowsFile } from './windows-file-security'

const directoryWriteRights = ['write', 'append', 'delete', 'add_file', 'add_subdirectory', 'writeattr', 'writeextattr', 'writesecurity', 'chown', 'delete_child']
const fileWriteRights = ['write', 'append', 'delete', 'writeattr', 'writeextattr', 'writesecurity', 'chown']
const privateFileRights = ['read', ...fileWriteRights]

// Check the entire namespace, not just the mode of the final credential file.
// Root-owned sticky temporary directories are allowed: an unprivileged user
// cannot replace another user's child there. User-owned symlinks are rejected.
export async function checkDirectory(path: string): Promise<void> {
  const absolute = resolve(path)
  if (process.platform === 'win32') {
    const info = await lstat(absolute)
    if (!info.isDirectory() || info.isSymbolicLink()) throw new Error(`unsafe runtime directory: ${absolute}`)
    return checkWindowsAccess(absolute, false)
  }
  const checked = new Set<string>()
  await inspectDirectory(absolute, checked)
  if (process.platform === 'darwin' && await grantsOthers([...checked], directoryWriteRights)) {
    throw new Error(`runtime directory chain has a writable extended ACL: ${absolute}`)
  }
}

// An ACL entry the user granted to themselves adds nothing another account
// could abuse, so only entries for other principals count.
async function grantsOthers(paths: string[], rights: string[]): Promise<boolean> {
  const { stdout } = await promisify(execFile)('/bin/ls', ['-lde', ...paths])
  const self = userInfo().username
  for (const line of stdout.split('\n').filter(line => /^\s*\d+:/.test(line))) {
    // ls 会把继承标记放在主体与 allow/deny 之间；不能忽略这类条目。
    const entry = line.match(/^\s*\d+:\s+(user|group):(.+?)\s+(?:inherited\s+)?(allow|deny)\s+(\S+)\s*$/)
    if (!entry) throw new Error(`could not verify macOS ACL entry for runtime path: ${paths.join(', ')}`)
    const [, kind, principal, access, granted] = entry
    if (access === 'deny') continue
    if (kind === 'user' && principal === self) continue
    if (granted.split(',').some(right => rights.includes(right))) return true
  }
  return false
}

async function inspectDirectory(absolute: string, checked: Set<string>): Promise<void> {
  if (checked.has(absolute)) return
  checked.add(absolute)
  const parent = dirname(absolute)
  if (parent !== absolute) await inspectDirectory(parent, checked)
  const info = await lstat(absolute)
  if (info.isSymbolicLink() && info.uid === 0) {
    return inspectDirectory(await realpath(absolute), checked)
  }
  if (!info.isDirectory() || info.isSymbolicLink()) throw new Error(`unsafe runtime directory: ${absolute}`)
  const uid = process.getuid!()
  const trustedOwner = info.uid === uid || info.uid === 0
  const stickyRoot = info.uid === 0 && (info.mode & 0o1000) !== 0
  if (!trustedOwner || ((info.mode & 0o022) !== 0 && !stickyRoot)) {
    throw new Error(`runtime directory must be owned by you or root and not writable by other users: ${absolute}`)
  }
}

export async function ensureDirectory(path: string): Promise<void> {
  const absolute = resolve(path)
  try {
    await checkDirectory(absolute)
  } catch (error) {
    if (errorCode(error) !== 'ENOENT') throw error
    const parent = dirname(absolute)
    if (parent === absolute || absolute === parse(absolute).root) throw error
    await ensureDirectory(parent)
    try { await mkdir(absolute, { mode: 0o700 }) } catch (error) {
      if (errorCode(error) !== 'EEXIST') throw error
    }
    await checkDirectory(absolute)
  }
}

export async function readPrivateFile(path: string): Promise<string> {
  await checkDirectory(dirname(path))
  const info = await lstat(path)
  if (!info.isFile() || info.isSymbolicLink()
    || (process.platform !== 'win32' && (info.uid !== process.getuid!() || (info.mode & 0o077) !== 0))) {
    throw new Error(`runtime file must be a private regular file owned by you: ${path}`)
  }
  if (process.platform === 'darwin' && await grantsOthers([path], privateFileRights)) {
    throw new Error(`runtime file has an unsafe extended ACL: ${path}`)
  }
  if (process.platform === 'win32') await checkWindowsAccess(path, true)
  const handle = await open(path, constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0))
  try {
    const opened = await handle.stat()
    if (opened.dev !== info.dev || opened.ino !== info.ino) throw new Error(`runtime file changed while opening: ${path}`)
    return await handle.readFile('utf8')
  } finally { await handle.close() }
}

export async function writeFileAtomic(path: string, content: string | Uint8Array, mode: number): Promise<void> {
  await ensureDirectory(dirname(path))
  const staging = join(dirname(path), `.${randomUUID()}.tmp`)
  // Inherited read ACLs can let another account pre-open an empty file and
  // retain that handle after its ACL is tightened. Restrict an empty directory
  // first so the file is born private, even for existing directory handles.
  await mkdir(staging, { mode: 0o700 })
  const temporary = join(staging, 'content')
  try {
    if (process.platform === 'darwin') await promisify(execFile)('/bin/chmod', ['-N', staging])
    if (process.platform === 'win32') await protectWindowsDirectory(staging)
    const handle = await open(temporary, 'wx', mode)
    try {
      if (process.platform === 'darwin') await promisify(execFile)('/bin/chmod', ['-N', temporary])
      if (process.platform === 'win32') await protectWindowsFile(temporary)
      await handle.writeFile(content)
      await handle.sync()
    } finally { await handle.close() }
    await chmod(temporary, mode)
    // Never unlink a committed destination to work around a failed rename.
    await rename(temporary, path)
    if (process.platform !== 'win32') {
      const directory = await open(dirname(path), constants.O_RDONLY)
      try { await directory.sync() } finally { await directory.close() }
    }
  } finally { await rm(staging, { recursive: true, force: true }) }
}

export function errorCode(error: unknown): string | undefined {
  return error && typeof error === 'object' && 'code' in error ? String(error.code) : undefined
}
