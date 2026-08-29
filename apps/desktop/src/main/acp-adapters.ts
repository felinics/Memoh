import { execFile } from 'node:child_process'
import { constants } from 'node:fs'
import { access, readdir, stat } from 'node:fs/promises'
import { delimiter, isAbsolute, join } from 'node:path'
import { promisify } from 'node:util'

import type { TrustedACPLaunchers } from '@memohai/runtime'

const execFileAsync = promisify(execFile)
const loginShellProbeTimeoutMs = 5_000

const adapterEntries = {
  'codex-acp': ['@agentclientprotocol', 'codex-acp', 'dist', 'index.js'],
  'claude-agent-acp': ['@agentclientprotocol', 'claude-agent-acp', 'dist', 'index.js'],
} as const

export interface BundledACPLauncherOptions {
  platform: NodeJS.Platform
  electronExecutable: string
  appPath: string
  resourcesPath: string
  isPackaged: boolean
  homeDirectory: string
  pathValue?: string
  /**
   * Login shell used to resolve the user's real PATH (Finder-launched apps
   * inherit only the system default). Pass false to skip the probe in tests.
   */
  loginShell?: string | false
}

/**
 * Resolves the fixed adapters bundled with Desktop and the user's local
 * Codex/Claude entry points. Paths stay in Electron Main and are never sent
 * to the renderer or Memoh Server.
 *
 * Discovery order per CLI:
 * 1. a login-shell `command -v` probe, which sees the PATH the user's login
 *    profile builds (volta, pnpm, mise, homebrew, ...);
 * 2. a deterministic fallback list — fixed directories plus enumerated
 *    nvm/fnm node installs, whose PATH lives in interactive rc files the
 *    login probe cannot see.
 *
 * Found paths are validated but deliberately not realpath-resolved: version
 * managers like volta dispatch on the symlink's basename, and resolving the
 * link would record the shim binary under the wrong name.
 */
export async function discoverBundledACPLaunchers(
  options: BundledACPLauncherOptions,
): Promise<TrustedACPLaunchers> {
  if (options.platform !== 'darwin' && options.platform !== 'linux') {
    return disabledLaunchers()
  }
  const electronExecutable = assertAbsolute(options.electronExecutable, 'Electron executable')
  const applicationRoot = options.isPackaged
    ? join(assertAbsolute(options.resourcesPath, 'resources path'), 'app.asar')
    : assertAbsolute(options.appPath, 'application path')
  const homeDirectory = assertAbsolute(options.homeDirectory, 'home directory')
  const probed = await probeLoginShell(options)
  const directories = executableSearchDirectories(
    homeDirectory,
    options.pathValue,
    await versionManagerDirectories(homeDirectory),
  )
  const [codexExecutable, claudeCodeExecutable] = await Promise.all([
    resolveExecutable('codex', probed, directories),
    resolveExecutable('claude', probed, directories),
  ])

  return Object.freeze({
    'codex-acp': codexExecutable
      ? Object.freeze({
          nodeExecutable: electronExecutable,
          adapterEntry: join(applicationRoot, 'node_modules', ...adapterEntries['codex-acp']),
          codexExecutable,
        })
      : false,
    'claude-agent-acp': claudeCodeExecutable
      ? Object.freeze({
          nodeExecutable: electronExecutable,
          adapterEntry: join(
            applicationRoot,
            'node_modules',
            ...adapterEntries['claude-agent-acp'],
          ),
          claudeCodeExecutable,
        })
      : false,
  })
}

function disabledLaunchers(): TrustedACPLaunchers {
  return Object.freeze({
    'codex-acp': false,
    'claude-agent-acp': false,
  })
}

/**
 * Asks the user's login shell where `codex` and `claude` live. `-l` sources
 * the login profile (where PATH additions from version managers live) without
 * `-i`, which would pull in interactive-only config and TTY expectations.
 */
async function probeLoginShell(
  options: BundledACPLauncherOptions,
): Promise<ReadonlyMap<'codex' | 'claude', string>> {
  const found = new Map<'codex' | 'claude', string>()
  const shell = options.loginShell === false
    ? undefined
    : options.loginShell ?? process.env.SHELL
  if (!shell || !isAbsolute(shell)) {
    return found
  }
  let stdout: string
  try {
    ({ stdout } = await execFileAsync(
      shell,
      ['-lc', 'printf "codex:%s\\nclaude:%s\\n" "$(command -v codex || true)" "$(command -v claude || true)"'],
      // SIGKILL: a profile that traps TERM must not wedge the connect loop
      // past the timeout.
      { timeout: loginShellProbeTimeoutMs, killSignal: 'SIGKILL', windowsHide: true },
    ))
  } catch {
    // A hanging or failing login shell falls back to the directory list.
    return found
  }
  for (const line of stdout.split('\n')) {
    const match = /^(codex|claude):(\/.+)$/.exec(line.trim())
    if (!match) continue
    const name = match[1] as 'codex' | 'claude'
    if (!found.has(name) && await isExecutableFile(match[2])) {
      found.set(name, match[2])
    }
  }
  return found
}

// nvm and fnm set PATH from interactive rc files (~/.zshrc, ~/.bashrc) that a
// non-interactive login shell does not source, so their node installs are
// enumerated directly. Newest version first, matching what the user's shell
// would most likely resolve.
async function versionManagerDirectories(homeDirectory: string): Promise<readonly string[]> {
  const layouts = [
    { root: join(homeDirectory, '.nvm', 'versions', 'node'), suffix: ['bin'] },
    { root: join(homeDirectory, '.local', 'share', 'fnm', 'node-versions'), suffix: ['installation', 'bin'] },
    { root: join(homeDirectory, 'Library', 'Application Support', 'fnm', 'node-versions'), suffix: ['installation', 'bin'] },
  ]
  const directories: string[] = []
  for (const { root, suffix } of layouts) {
    let entries: string[]
    try {
      entries = await readdir(root)
    } catch {
      continue
    }
    const versions = entries
      .filter(entry => /^v?\d/.test(entry))
      .sort((left, right) => right.localeCompare(left, undefined, { numeric: true }))
    for (const version of versions) {
      directories.push(join(root, version, ...suffix))
    }
  }
  return directories
}

function executableSearchDirectories(
  homeDirectory: string,
  pathValue?: string,
  versionManagerBins: readonly string[] = [],
): readonly string[] {
  const knownDirectories = [
    join(homeDirectory, '.local', 'bin'),
    join(homeDirectory, '.npm-global', 'bin'),
    join(homeDirectory, '.bun', 'bin'),
    join(homeDirectory, '.volta', 'bin'),
    join(homeDirectory, '.local', 'share', 'mise', 'shims'),
    join(homeDirectory, '.local', 'share', 'pnpm'),
    join(homeDirectory, 'Library', 'pnpm'),
    '/opt/homebrew/bin',
    '/usr/local/bin',
    '/usr/bin',
    '/bin',
  ]
  const inheritedDirectories = (pathValue ?? '')
    .split(delimiter)
    .map(value => value.trim())
    .filter(value => value.length > 0 && isAbsolute(value))
  return Object.freeze([...new Set([...knownDirectories, ...versionManagerBins, ...inheritedDirectories])])
}

async function resolveExecutable(
  name: 'codex' | 'claude',
  probed: ReadonlyMap<'codex' | 'claude', string>,
  directories: readonly string[],
): Promise<string | undefined> {
  const fromShell = probed.get(name)
  if (fromShell) {
    return fromShell
  }
  for (const directory of directories) {
    const executable = join(directory, name)
    if (await isExecutableFile(executable)) {
      return executable
    }
  }
  return undefined
}

async function isExecutableFile(path: string): Promise<boolean> {
  try {
    // stat/access follow symlinks, validating the target while keeping the
    // recorded path the user-facing one.
    const entry = await stat(path)
    if (!entry.isFile()) return false
    await access(path, constants.X_OK)
    return true
  } catch {
    return false
  }
}

function assertAbsolute(value: string, label: string): string {
  if (!isAbsolute(value)) throw new Error(`the Desktop ${label} must be absolute`)
  return value
}
