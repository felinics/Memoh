import { constants, type Stats } from 'node:fs'
import { randomBytes } from 'node:crypto'
import {
  lstat,
  open,
  readdir,
  rename as renamePath,
  rm,
  rmdir,
  stat,
  unlink,
} from 'node:fs/promises'
import { basename, dirname, join, relative, sep } from 'node:path'
import type { FileHandle } from 'node:fs/promises'

import { status } from '@grpc/grpc-js'

import { isServiceError, mapNodeError, nodeErrorCode, rpcError } from '../rpc'
import type {
  DeleteFileRequest,
  FileEntry,
  ListDirRequest,
  MkdirRequest,
  ReadFileRequest,
  RenameRequest,
  StatRequest,
  WriteFileRequest,
} from '../types'
import { HostPathResolver } from './paths'

const binaryProbeBytes = 8 * 1024
const readMaxLines = 2_000
const listMaxEntries = 200
const defaultRecursiveMaxEntries = 100_000
const readResponseMaxBytes = 16 * 1024 * 1024 - 1_024
export const rawChunkSize = 64 * 1024

export interface WorkspaceFileServiceOptions {
  defaultMaxRecursiveEntries?: number
}

export class WorkspaceFileService {
  private readonly defaultMaxRecursiveEntries: number

  constructor(readonly paths: HostPathResolver, options: WorkspaceFileServiceOptions = {}) {
    this.defaultMaxRecursiveEntries = options.defaultMaxRecursiveEntries ?? defaultRecursiveMaxEntries
  }

  async readFile(request: ReadFileRequest): Promise<{ content: string, total_lines: number, binary: boolean }> {
    const requested = requiredPath(request.path)
    const target = await this.paths.resolve(requested)
    await this.paths.revalidate(target)
    const handle = await openNoFollow(target, constants.O_RDONLY, 0).catch(error => {
      throw mapNodeError(error, 'open')
    })
    try {
      const probe = Buffer.alloc(binaryProbeBytes)
      const { bytesRead } = await handle.read(probe, 0, probe.length, 0)
      if (probe.subarray(0, bytesRead).includes(0)) {
        return { content: '', total_lines: 0, binary: true }
      }

      let lineOffset = request.line_offset ?? 0
      if (lineOffset < 1) {
        lineOffset = 1
      }
      let lineLimit = request.n_lines ?? 0
      if (lineLimit < 1 || lineLimit > readMaxLines) {
        lineLimit = readMaxLines
      }

      let totalLines = 0
      let selected = 0
      let content = ''
      let contentBytes = 0
      for await (const line of scanLines(handle)) {
        totalLines++
        if (totalLines >= lineOffset && selected < lineLimit) {
          const entry = `${line}\n`
          const entryBytes = Buffer.byteLength(entry, 'utf8')
          if (contentBytes + entryBytes > readResponseMaxBytes) {
            throw rpcError(status.RESOURCE_EXHAUSTED, 'read response exceeds the 16 MiB gRPC message limit')
          }
          content += entry
          contentBytes += entryBytes
          selected++
        }
      }
      return { content, total_lines: totalLines, binary: false }
    } catch (error) {
      throw mapNodeError(error, 'read')
    } finally {
      await handle.close().catch(() => undefined)
    }
  }

  async writeFile(request: WriteFileRequest): Promise<Record<string, never>> {
    const requested = requiredPath(request.path)
    const target = await this.paths.prepareWrite(requested)
    const temporary = join(
      dirname(target),
      `.${basename(target)}.tmp-${randomBytes(8).toString('hex')}`,
    )
    const handle = await openNoFollow(
      temporary,
      constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL,
      0o600,
    ).catch(error => {
      throw mapNodeError(error, 'open')
    })
    try {
      await handle.writeFile(Buffer.from(request.content ?? Buffer.alloc(0)))
      await handle.close()
      await this.paths.revalidate(target, { allowMissing: true })
      await renamePath(temporary, target)
    } catch (error) {
      throw mapNodeError(error, 'write')
    } finally {
      await handle.close().catch(() => undefined)
      await rm(temporary, { force: true }).catch(() => undefined)
    }
    return {}
  }

  async listDir(request: ListDirRequest, signal?: AbortSignal): Promise<{
    entries: FileEntry[]
    total_count: number
    truncated: boolean
  }> {
    if (signal?.aborted) {
      throw rpcError(status.CANCELLED, 'request cancelled')
    }
    const requested = request.path?.trim() || '.'
    const directory = await this.paths.resolve(requested, { requireDirectory: true })
    await this.paths.revalidate(directory, { requireDirectory: true })
    const maxEntries = request.recursive
      ? ((request.max_entries ?? 0) > 0 ? request.max_entries! : this.defaultMaxRecursiveEntries)
      : ((request.max_entries ?? 0) > 0 ? request.max_entries! : 0)
    let entries = request.recursive
      ? await this.walk(directory, maxEntries, signal)
      : await this.readDirectory(directory, maxEntries, signal)

    const threshold = request.collapse_threshold ?? 0
    if (request.recursive && threshold > 0) {
      entries = collapseHeavySubdirectories(entries, threshold)
    }

    const totalCount = Math.min(entries.length, 2_147_483_647)
    const offset = Math.max(0, request.offset ?? 0)
    let limit = Math.max(0, request.limit ?? 0)
    if (limit > listMaxEntries) {
      limit = listMaxEntries
    }
    const end = limit > 0 ? offset + limit : undefined
    const page = offset < entries.length ? entries.slice(offset, end) : []
    return {
      entries: page,
      total_count: totalCount,
      truncated: offset + page.length < totalCount,
    }
  }

  async stat(request: StatRequest): Promise<{ entry: FileEntry }> {
    const target = await this.paths.resolve(requiredPath(request.path))
    await this.paths.revalidate(target)
    try {
      const info = await stat(target)
      return { entry: fileEntry(basename(target), info) }
    } catch (error) {
      throw mapNodeError(error, 'stat')
    }
  }

  async mkdir(request: MkdirRequest): Promise<Record<string, never>> {
    await this.paths.ensureDirectory(requiredPath(request.path))
    return {}
  }

  async rename(request: RenameRequest): Promise<Record<string, never>> {
    const oldPath = requiredPath(request.old_path, 'old_path')
    const newPath = requiredPath(request.new_path, 'new_path')
    const source = await this.paths.resolve(oldPath, { followFinalSymlink: false })
    const destination = await this.paths.prepareWrite(newPath)
    await this.paths.revalidate(source, { followFinalSymlink: false })
    await this.paths.revalidate(destination, { allowMissing: true })
    try {
      await renamePath(source, destination)
    } catch (error) {
      throw mapNodeError(error, 'rename')
    }
    return {}
  }

  async deleteFile(request: DeleteFileRequest): Promise<Record<string, never>> {
    const target = await this.paths.resolve(requiredPath(request.path), {
      allowMissing: true,
      followFinalSymlink: false,
    })
    try {
      const info = await lstat(target)
      if (info.isSymbolicLink()) {
        await unlink(target)
        return {}
      }
      await this.paths.revalidate(target)
      if (info.isDirectory() && !request.recursive) {
        await rmdir(target)
      } else if (info.isDirectory()) {
        await rm(target, { recursive: true, force: true })
      } else {
        await unlink(target)
      }
    } catch (error) {
      if (nodeErrorCode(error) !== 'ENOENT' && !(isServiceError(error) && error.code === status.NOT_FOUND)) {
        throw mapNodeError(error, 'delete')
      }
    }
    return {}
  }

  async openReadRaw(requestedPath: string) {
    const target = await this.paths.resolve(requiredPath(requestedPath))
    await this.paths.revalidate(target)
    return openNoFollow(target, constants.O_RDONLY, 0).catch(error => {
      throw mapNodeError(error, 'open')
    })
  }

  async openWriteRaw(requestedPath: string) {
    const target = await this.paths.prepareWrite(requiredPath(requestedPath))
    await this.paths.revalidate(target, { allowMissing: true })
    return openNoFollow(
      target,
      constants.O_WRONLY | constants.O_CREAT | constants.O_TRUNC,
      0o600,
    ).catch(error => {
      throw mapNodeError(error, 'open')
    })
  }

  private async readDirectory(directory: string, maxEntries: number, signal?: AbortSignal): Promise<FileEntry[]> {
    let children
    try {
      children = await readdir(directory, { withFileTypes: true })
    } catch (error) {
      throw mapNodeError(error, 'readdir')
    }
    children.sort((left, right) => compareGoFilenames(left.name, right.name))
    const entries: FileEntry[] = []
    for (const child of children) {
      if (signal?.aborted) {
        throw rpcError(status.CANCELLED, 'request cancelled')
      }
      const target = join(directory, child.name)
      let info: Stats
      try {
        info = await lstat(target)
      } catch {
        // Match filepath.WalkDir's best-effort behavior for entries that vanish.
        continue
      }
      if (maxEntries > 0 && entries.length >= maxEntries) {
        throw rpcError(status.RESOURCE_EXHAUSTED, `directory listing exceeds ${maxEntries} entries`)
      }
      entries.push(fileEntry(child.name, info))
    }
    return entries
  }

  private async walk(directory: string, maxEntries: number, signal?: AbortSignal): Promise<FileEntry[]> {
    const entries: FileEntry[] = []
    const visit = async (current: string): Promise<void> => {
      if (signal?.aborted) {
        throw rpcError(status.CANCELLED, 'request cancelled')
      }
      let children
      try {
        children = await readdir(current, { withFileTypes: true })
      } catch {
        return
      }
      children.sort((left, right) => compareGoFilenames(left.name, right.name))
      for (const child of children) {
        if (signal?.aborted) {
          throw rpcError(status.CANCELLED, 'request cancelled')
        }
        const target = join(current, child.name)
        let info: Stats
        try {
          info = await lstat(target)
        } catch {
          // A disappearing or unreadable entry is skipped without aborting the walk.
          continue
        }
        if (maxEntries > 0 && entries.length >= maxEntries) {
          throw rpcError(status.RESOURCE_EXHAUSTED, `recursive listing exceeds ${maxEntries} entries`)
        }
        const name = relative(directory, target).split(sep).join('/')
        entries.push(fileEntry(name, info))
        if (info.isDirectory() && !info.isSymbolicLink()) {
          await visit(target)
        }
      }
    }
    await visit(directory)
    return entries
  }
}

function requiredPath(value: string | undefined, field = 'path'): string {
  if (!value?.trim()) {
    throw rpcError(status.INVALID_ARGUMENT, `${field} is required`)
  }
  return value
}

function openNoFollow(path: string, flags: number, mode: number) {
  const noFollow = process.platform === 'win32' ? 0 : constants.O_NOFOLLOW
  return open(path, flags | noFollow, mode)
}

function fileEntry(path: string, info: Stats): FileEntry {
  return {
    path,
    is_dir: info.isDirectory(),
    size: String(info.size),
    mode: formatMode(info.mode, info.isDirectory(), info.isSymbolicLink()),
    mod_time: formatTimestamp(info.mtime),
    summary: '',
  }
}

export function compareGoFilenames(left: string, right: string): number {
  if (left === right) return 0
  // os.ReadDir/filepath.WalkDir compare UTF-8 bytes. For valid Unicode scalar
  // values UTF-8 byte order equals codepoint order; lone surrogates fall back to
  // exact Buffer encoding so WTF-16 edge cases stay bit-identical to Go strings.
  let i = 0
  let j = 0
  while (i < left.length && j < right.length) {
    const a = left.codePointAt(i)!
    const b = right.codePointAt(j)!
    if ((a >= 0xd800 && a <= 0xdfff) || (b >= 0xd800 && b <= 0xdfff)) {
      return Buffer.compare(Buffer.from(left, 'utf8'), Buffer.from(right, 'utf8'))
    }
    if (a !== b) return a < b ? -1 : 1
    i += a > 0xffff ? 2 : 1
    j += b > 0xffff ? 2 : 1
  }
  return i < left.length ? 1 : -1
}

export function formatTimestamp(value: Date): string {
  const year = String(value.getFullYear()).padStart(4, '0')
  const month = String(value.getMonth() + 1).padStart(2, '0')
  const day = String(value.getDate()).padStart(2, '0')
  const hour = String(value.getHours()).padStart(2, '0')
  const minute = String(value.getMinutes()).padStart(2, '0')
  const second = String(value.getSeconds()).padStart(2, '0')
  const offsetMinutes = -value.getTimezoneOffset()
  const zone = offsetMinutes === 0
    ? 'Z'
    : `${offsetMinutes < 0 ? '-' : '+'}${String(Math.floor(Math.abs(offsetMinutes) / 60)).padStart(2, '0')}:${String(Math.abs(offsetMinutes) % 60).padStart(2, '0')}`
  return `${year}-${month}-${day}T${hour}:${minute}:${second}${zone}`
}

export function formatMode(mode: number, directory = false, symbolicLink = false): string {
  const type = mode & 0o170000
  const isDirectory = directory || type === 0o040000
  const isSymbolicLink = symbolicLink || type === 0o120000
  const isBlockDevice = type === 0o060000
  const isCharacterDevice = type === 0o020000
  const isNamedPipe = type === 0o010000
  const isSocket = type === 0o140000
  const knownType = type === 0 || type === 0o100000 || isDirectory || isSymbolicLink
    || isBlockDevice || isCharacterDevice || isNamedPipe || isSocket
  let prefix = ''
  if (isDirectory) {
    prefix += 'd'
  }
  if (isSymbolicLink) {
    prefix += 'L'
  }
  if (isBlockDevice || isCharacterDevice) {
    prefix += 'D'
  }
  if (isNamedPipe) {
    prefix += 'p'
  }
  if (isSocket) {
    prefix += 'S'
  }
  if (mode & 0o4000) {
    prefix += 'u'
  }
  if (mode & 0o2000) {
    prefix += 'g'
  }
  if (isCharacterDevice) {
    prefix += 'c'
  }
  if (mode & 0o1000) {
    prefix += 't'
  }
  if (!knownType) {
    prefix += '?'
  }
  if (!prefix) {
    prefix = '-'
  }
  const permissions = [
    [constants.S_IRUSR, 'r'], [constants.S_IWUSR, 'w'], [constants.S_IXUSR, 'x'],
    [constants.S_IRGRP, 'r'], [constants.S_IWGRP, 'w'], [constants.S_IXGRP, 'x'],
    [constants.S_IROTH, 'r'], [constants.S_IWOTH, 'w'], [constants.S_IXOTH, 'x'],
  ] as const
  return prefix + permissions.map(([bit, value]) => mode & bit ? value : '-').join('')
}

function collapseHeavySubdirectories(entries: FileEntry[], threshold: number): FileEntry[] {
  const counts = new Map<string, number>()
  for (const entry of entries) {
    const top = topDirectory(entry.path)
    if (top) {
      counts.set(top, (counts.get(top) ?? 0) + 1)
    }
  }
  const heavy = new Set([...counts].filter(([, count]) => count > threshold).map(([path]) => path))
  if (heavy.size === 0) {
    return entries
  }
  const seen = new Set<string>()
  const result: FileEntry[] = []
  for (const entry of entries) {
    const top = topDirectory(entry.path)
    if (!heavy.has(top)) {
      result.push(entry)
    } else if (entry.path === top && entry.is_dir) {
      result.push(entry)
    } else if (!seen.has(top)) {
      seen.add(top)
      result.push({
        path: `${top}/`,
        is_dir: true,
        size: '0',
        mode: '',
        mod_time: '',
        summary: `${counts.get(top)} items (not expanded)`,
      })
    }
  }
  return result
}

function topDirectory(path: string): string {
  const separator = path.indexOf('/')
  return separator >= 0 ? path.slice(0, separator) : ''
}

async function* scanLines(handle: FileHandle): AsyncGenerator<string> {
  const chunk = Buffer.alloc(64 * 1024)
  let carry = Buffer.alloc(0)
  let position = 0
  for (;;) {
    const { bytesRead } = await handle.read(chunk, 0, chunk.length, position)
    if (bytesRead === 0) {
      if (carry.length > 0 && carry.length <= 1024 * 1024) {
        yield decodeLine(carry)
      }
      return
    }
    position += bytesRead
    let buffer = carry.length === 0
      ? Buffer.from(chunk.subarray(0, bytesRead))
      : Buffer.concat([carry, chunk.subarray(0, bytesRead)])
    let newline = buffer.indexOf(0x0a)
    while (newline >= 0) {
      if (newline > 1024 * 1024) {
        return
      }
      yield decodeLine(buffer.subarray(0, newline))
      buffer = buffer.subarray(newline + 1)
      newline = buffer.indexOf(0x0a)
    }
    if (buffer.length > 1024 * 1024) {
      return
    }
    carry = Buffer.from(buffer)
  }
}

function decodeLine(line: Buffer): string {
  const content = line.at(-1) === 0x0d ? line.subarray(0, -1) : line
  return content.toString('utf8')
}
