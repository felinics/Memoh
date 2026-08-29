import { constants } from 'node:fs'
import {
  access,
  chmod,
  lstat,
  mkdtemp,
  open,
  readdir,
  rm,
  stat,
  writeFile,
} from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { isAbsolute, join } from 'node:path'

export const trustedACPAdapterNames = [
  'codex-acp',
  'claude-agent-acp',
] as const

export type TrustedACPAdapterName = typeof trustedACPAdapterNames[number]

interface TrustedACPLauncherBase {
  /** Absolute Electron or Node executable used only for Runtime's bootstrap. */
  nodeExecutable: string
  /** Absolute JavaScript entry file for the pinned ACP adapter package. */
  adapterEntry: string
}

export interface TrustedCodexACPLauncher extends TrustedACPLauncherBase {
  /** Absolute local Codex CLI entry; exposed only as CODEX_PATH. */
  codexExecutable: string
}

export interface TrustedClaudeCodeACPLauncher extends TrustedACPLauncherBase {
  /** Absolute local Claude Code entry; exposed only to this adapter. */
  claudeCodeExecutable: string
}

/** Main-process-only descriptors for the two fixed Desktop ACP adapters. */
export type TrustedACPLaunchers = Readonly<{
  /** false explicitly disables this alias and blocks ambient PATH fallback. */
  'codex-acp'?: TrustedCodexACPLauncher | false
  /** false explicitly disables this alias and blocks ambient PATH fallback. */
  'claude-agent-acp'?: TrustedClaudeCodeACPLauncher | false
}>

export interface PreparedTrustedACPLaunchers {
  configuredAdapters: readonly TrustedACPAdapterName[]
  availableAdapters: readonly TrustedACPAdapterName[]
  executableDirectories: readonly string[]
  close(): Promise<void>
}

interface PrepareTrustedACPLaunchersOptions {
  os?: NodeJS.Platform
  warn?: (message: string) => void
}

interface NormalizedLauncher {
  nodeExecutable: string
  adapterEntry: string
  agentExecutable: string
}

const launcherDirectoryPrefix = 'memoh-runtime-acp-'

/**
 * Copies and validates the embedding-process configuration before a session
 * starts. There are no generic aliases, arbitrary argv, or arbitrary env
 * values: each fixed alias has one strongly typed local-Agent path.
 */
export function normalizeTrustedACPLaunchers(
  launchers: TrustedACPLaunchers | undefined,
): TrustedACPLaunchers {
  if (launchers === undefined) {
    return Object.freeze({})
  }
  assertRecord(launchers, 'trustedACPLaunchers must be an object')

  const normalized: {
    'codex-acp'?: TrustedCodexACPLauncher | false
    'claude-agent-acp'?: TrustedClaudeCodeACPLauncher | false
  } = {}
  for (const key of Object.keys(launchers)) {
    if (!isTrustedACPAdapterName(key)) {
      throw new Error(`unsupported trusted ACP adapter alias: ${key}`)
    }
    const launcher = launchers[key]
    if (launcher === false) {
      normalized[key] = false
      continue
    }
    assertRecord(launcher, `trusted ACP launcher ${key} must be an object`)
    const allowedFields = key === 'codex-acp'
      ? new Set(['nodeExecutable', 'adapterEntry', 'codexExecutable'])
      : new Set(['nodeExecutable', 'adapterEntry', 'claudeCodeExecutable'])
    for (const field of Object.keys(launcher)) {
      if (!allowedFields.has(field)) {
        throw new Error(`trusted ACP launcher ${key} field ${field} is not allowed`)
      }
    }
    const nodeExecutable = absoluteLauncherPath(launcher.nodeExecutable, `${key} nodeExecutable`)
    const adapterEntry = absoluteLauncherPath(launcher.adapterEntry, `${key} adapterEntry`)
    if (key === 'codex-acp') {
      normalized[key] = Object.freeze({
        nodeExecutable,
        adapterEntry,
        codexExecutable: absoluteLauncherPath(launcher.codexExecutable, `${key} codexExecutable`),
      })
    } else {
      normalized[key] = Object.freeze({
        nodeExecutable,
        adapterEntry,
        claudeCodeExecutable: absoluteLauncherPath(
          launcher.claudeCodeExecutable,
          `${key} claudeCodeExecutable`,
        ),
      })
    }
  }
  return Object.freeze(normalized)
}

/**
 * Verifies every fixed file before materializing mode-0700 POSIX shims. An
 * explicitly configured but invalid alias remains configured, so capability
 * detection cannot fall back to a same-name executable from the ambient GUI
 * PATH.
 */
export async function prepareTrustedACPLaunchers(
  launchers: TrustedACPLaunchers | undefined,
  options: PrepareTrustedACPLaunchersOptions = {},
): Promise<PreparedTrustedACPLaunchers> {
  const normalized = normalizeTrustedACPLaunchers(launchers)
  const configuredAdapters = trustedACPAdapterNames.filter(name => (
    Object.prototype.hasOwnProperty.call(normalized, name)
  ))
  const os = options.os ?? process.platform
  if (configuredAdapters.length === 0) {
    return emptyPreparedLaunchers(configuredAdapters)
  }
  if (os !== 'darwin' && os !== 'linux') {
    for (const name of configuredAdapters) {
      options.warn?.(`trusted ACP launcher ${name} is unavailable on ${os}`)
    }
    return emptyPreparedLaunchers(configuredAdapters)
  }

  const verified = new Map<TrustedACPAdapterName, NormalizedLauncher>()
  for (const name of configuredAdapters) {
    const launcher = normalized[name]
    if (!launcher) continue
    try {
      verified.set(name, {
        nodeExecutable: await verifiedFile(launcher.nodeExecutable, constants.X_OK, 'node executable'),
        adapterEntry: await verifiedFile(launcher.adapterEntry, constants.R_OK, 'adapter entry'),
        agentExecutable: await verifiedFile(
          name === 'codex-acp'
            ? (normalized['codex-acp'] as TrustedCodexACPLauncher).codexExecutable
            : (normalized['claude-agent-acp'] as TrustedClaudeCodeACPLauncher).claudeCodeExecutable,
          constants.X_OK,
          'agent executable',
        ),
      })
    } catch (error) {
      options.warn?.(`trusted ACP launcher ${name} is unavailable: ${errorMessage(error)}`)
    }
  }
  if (verified.size === 0) {
    return emptyPreparedLaunchers(configuredAdapters)
  }

  const directory = await mkdtemp(join(tmpdir(), `${launcherDirectoryPrefix}${process.pid}-`))
  let closed = false
  const close = async () => {
    if (closed) return
    closed = true
    await rm(directory, { recursive: true, force: true })
  }
  try {
    await chmod(directory, 0o700)
    const availableAdapters: TrustedACPAdapterName[] = []
    for (const name of trustedACPAdapterNames) {
      const launcher = verified.get(name)
      if (!launcher) continue
      // One broken adapter degrades to a warning like verification failures
      // do; it must not fail the whole connection loop for the other alias.
      try {
        const agentCommand = await materializeAgentCommand(directory, name, launcher)
        const bootstrap = join(directory, `${name}-bootstrap.mjs`)
        const shim = join(directory, name)
        await writePrivateFile(bootstrap, launcherBootstrap(name, {
          ...launcher,
          agentExecutable: agentCommand,
        }))
        await writePrivateFile(shim, launcherShim(launcher.nodeExecutable, bootstrap))
        availableAdapters.push(name)
      } catch (error) {
        options.warn?.(`trusted ACP launcher ${name} could not be prepared: ${errorMessage(error)}`)
      }
    }
    if (availableAdapters.length === 0) {
      await close()
      return emptyPreparedLaunchers(configuredAdapters)
    }
    return {
      configuredAdapters: Object.freeze([...configuredAdapters]),
      availableAdapters: Object.freeze(availableAdapters),
      executableDirectories: Object.freeze([directory]),
      close,
    }
  } catch (error) {
    await close().catch(() => undefined)
    throw error
  }
}

/** Removes private launcher state owned by Runtime processes that no longer exist. */
export async function cleanupStaleTrustedACPLaunchers(): Promise<void> {
  let names: string[]
  try {
    names = await readdir(tmpdir())
  } catch {
    return
  }
  await Promise.all(names.map(async name => {
    const match = /^memoh-runtime-acp-(\d+)-[0-9A-Za-z_-]{6}$/.exec(name)
    if (!match) return
    const ownerPID = Number(match[1])
    if (!Number.isSafeInteger(ownerPID) || ownerPID <= 0 || processIsAlive(ownerPID)) return
    const path = join(tmpdir(), name)
    try {
      const entry = await lstat(path)
      if (!entry.isDirectory() || entry.isSymbolicLink()) return
      await rm(path, { recursive: true, force: true })
    } catch {
      // Best effort: another Runtime or cleanup pass may win this race.
    }
  }))
}

function emptyPreparedLaunchers(
  configuredAdapters: readonly TrustedACPAdapterName[],
): PreparedTrustedACPLaunchers {
  return {
    configuredAdapters: Object.freeze([...configuredAdapters]),
    availableAdapters: Object.freeze([]),
    executableDirectories: Object.freeze([]),
    close: async () => undefined,
  }
}

function absoluteLauncherPath(value: unknown, label: string): string {
  if (
    typeof value !== 'string'
    || value.length === 0
    || value.length > 4_096
    || value !== value.trim()
    || /[\0\r\n]/.test(value)
    || !isAbsolute(value)
  ) {
    throw new Error(`trusted ACP launcher ${label} must be a safe absolute path`)
  }
  return value
}

async function verifiedFile(
  path: string,
  mode: number,
  description: string,
): Promise<string> {
  try {
    // stat/access follow symlinks, so the target is fully validated — but the
    // returned path stays the original: version managers like volta dispatch
    // on the symlink's basename, and resolving it would launch the shim
    // binary under the wrong name.
    const entry = await stat(path)
    if (!entry.isFile()) {
      throw new Error(`${description} is not a regular file`)
    }
    await access(path, mode)
    return path
  } catch (error) {
    if (error instanceof Error && error.message === `${description} is not a regular file`) {
      throw error
    }
    throw new Error(`${description} is not accessible`)
  }
}

async function writePrivateFile(path: string, content: string): Promise<void> {
  await writeFile(path, content, {
    encoding: 'utf8',
    flag: 'wx',
    mode: 0o700,
  })
  await chmod(path, 0o700)
}

function launcherShim(nodeExecutable: string, bootstrap: string): string {
  const invocation = [nodeExecutable, bootstrap].map(quotePOSIXShellArgument).join(' ')
  return `#!/bin/sh\nELECTRON_RUN_AS_NODE=1 exec ${invocation} "$@"\n`
}

async function materializeAgentCommand(
  directory: string,
  name: TrustedACPAdapterName,
  launcher: NormalizedLauncher,
): Promise<string> {
  if (!await isNodeScript(launcher.agentExecutable)) {
    return launcher.agentExecutable
  }
  const bootstrap = join(directory, `${name}-agent-bootstrap.mjs`)
  const shim = join(directory, `${name}-agent`)
  await writePrivateFile(bootstrap, agentBootstrap(launcher.agentExecutable))
  await writePrivateFile(shim, launcherShim(launcher.nodeExecutable, bootstrap))
  return shim
}

async function isNodeScript(file: string): Promise<boolean> {
  const handle = await open(file, 'r')
  try {
    const buffer = Buffer.alloc(256)
    const { bytesRead } = await handle.read(buffer, 0, buffer.length, 0)
    const firstLine = buffer.subarray(0, bytesRead).toString('utf8').split(/\r?\n/, 1)[0]
    return /^#!\s*(?:\/usr\/bin\/env(?:\s+-S)?\s+node(?:\s|$)|\S*\/node(?:\s|$))/.test(firstLine)
  } finally {
    await handle.close()
  }
}

function agentBootstrap(agentEntry: string): string {
  const entry = javascriptString(agentEntry)
  return `import { pathToFileURL } from 'node:url'

const agentEntry = ${entry}
const agentArgs = process.argv.slice(2)
delete process.env.ELECTRON_RUN_AS_NODE
delete process.env.CODEX_PATH
delete process.env.CLAUDE_CODE_EXECUTABLE
process.argv = [process.execPath, agentEntry, ...agentArgs]
await import(pathToFileURL(agentEntry).href)
`
}

function launcherBootstrap(name: TrustedACPAdapterName, launcher: NormalizedLauncher): string {
  const adapterEntry = javascriptString(launcher.adapterEntry)
  const agentExecutable = javascriptString(launcher.agentExecutable)
  const agentEnvironment = name === 'codex-acp'
    ? `process.env.CODEX_PATH = ${agentExecutable}`
    : `process.env.CLAUDE_CODE_EXECUTABLE = ${agentExecutable}`
  return `import { pathToFileURL } from 'node:url'

delete process.env.ELECTRON_RUN_AS_NODE
delete process.env.CODEX_PATH
delete process.env.CLAUDE_CODE_EXECUTABLE
${agentEnvironment}

const adapterEntry = ${adapterEntry}
const adapterArgs = process.argv.slice(2)
process.argv = [process.execPath, adapterEntry, ...adapterArgs]
await import(pathToFileURL(adapterEntry).href)
`
}

function javascriptString(value: string): string {
  return JSON.stringify(value).replaceAll('\u2028', '\\u2028').replaceAll('\u2029', '\\u2029')
}

function quotePOSIXShellArgument(value: string): string {
  return `'${value.replaceAll('\u0027', '\u0027"\u0027"\u0027')}'`
}

function isTrustedACPAdapterName(value: string): value is TrustedACPAdapterName {
  return (trustedACPAdapterNames as readonly string[]).includes(value)
}

function assertRecord(value: unknown, message: string): asserts value is Record<string, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new Error(message)
  }
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : 'validation failed'
}

function processIsAlive(pid: number): boolean {
  try {
    process.kill(pid, 0)
    return true
  } catch (error) {
    return (error as NodeJS.ErrnoException).code === 'EPERM'
  }
}
