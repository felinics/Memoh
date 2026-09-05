import { access, rm } from 'node:fs/promises'
import { homedir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parseArgs } from 'node:util'

import type { RuntimeClientConfig } from './config'
import {
  createRuntimeServiceManager,
  recordRuntimeServiceInstall,
  removeRuntimeInstallManifest,
  runtimeServiceSpec,
  secureWindowsCredentialFile,
  stageRuntimeArtifacts,
  spawnCommand,
  type CommandRunner,
  type RuntimeServiceManager,
} from './daemon'
import {
  normalizeRuntimeEnrollment,
  parseBooleanEnvironment,
  readInstallManifest,
  readRuntimeEnrollment,
  readRuntimeEnrollmentIfExists,
  resolveRuntimePaths,
  sameEnrollment,
  writeRuntimeEnrollment,
  type RuntimeEnrollment,
  type RuntimePaths,
} from './runtime-config'
import { RuntimeSession, type RuntimeSessionOptions } from './session'
import { runtimeClientVersion } from './version'

interface ManagedRuntimeSession {
  start(signal?: AbortSignal): Promise<void>
  stop(): void
}

type RuntimeSessionFactory = (
  config: RuntimeClientConfig,
  options: RuntimeSessionOptions,
) => ManagedRuntimeSession

export interface CLIContext {
  platform: NodeJS.Platform
  env: NodeJS.ProcessEnv
  home: string
  nodePath: string
  entryPath: string
  protoPath: string
  uid?: number
  runner: CommandRunner
  createSession: RuntimeSessionFactory
  stdout(message: string): void
  stderr(message: string): void
}

interface EnrollmentResolution {
  enrollment: RuntimeEnrollment
  provided: boolean
  providedByFlags: boolean
}

const connectionOptions = {
  server: { type: 'string' },
  key: { type: 'string' },
  'team-id': { type: 'string' },
  'runtime-id': { type: 'string' },
  config: { type: 'string' },
  'insecure-localhost': { type: 'boolean' },
  help: { type: 'boolean', short: 'h' },
} as const

export async function runCLI(args: string[], overrides: Partial<CLIContext> = {}): Promise<number> {
  const context = createCLIContext(overrides)
  if (args.includes('--workspace-base')) {
    throw new Error('--workspace-base is no longer supported; Remote Runtime uses the current user home directory')
  }
  if (args.length === 0) return runCommand([], context)
  const [command, ...rest] = args
  switch (command) {
    case '--help':
    case '-h':
    case 'help':
      context.stdout(rootUsage())
      return 0
    case '--version':
    case 'version':
      context.stdout(runtimeClientVersion)
      return 0
    case 'run':
      return runCommand(rest, context)
    case 'service':
      return serviceCommand(rest, context)
    default:
      // Backwards compatibility: the original CLI had no subcommand.
      if (command.startsWith('-')) return runCommand(args, context)
      throw new Error(`unknown command: ${command}`)
  }
}

function createCLIContext(overrides: Partial<CLIContext>): CLIContext {
  const entryPath = overrides.entryPath ?? fileURLToPath(import.meta.url)
  return {
    platform: overrides.platform ?? process.platform,
    env: overrides.env ?? process.env,
    home: overrides.home ?? homedir(),
    nodePath: overrides.nodePath ?? process.execPath,
    entryPath,
    protoPath: overrides.protoPath ?? join(dirname(entryPath), 'bridge.proto'),
    uid: overrides.uid ?? process.getuid?.(),
    runner: overrides.runner ?? spawnCommand,
    createSession: overrides.createSession ?? ((config, options) => new RuntimeSession(config, options)),
    stdout: overrides.stdout ?? (message => console.log(message)),
    stderr: overrides.stderr ?? (message => console.error(message)),
  }
}

async function runCommand(args: string[], context: CLIContext): Promise<number> {
  const { values } = parseArgs({
    args,
    strict: true,
    allowPositionals: false,
    options: {
      ...connectionOptions,
      save: { type: 'boolean' },
      'no-save': { type: 'boolean' },
    },
  })
  if (values.help) {
    context.stdout(runUsage())
    return 0
  }
  if (values.save && values['no-save']) throw new Error('--save and --no-save cannot be used together')
  const paths = pathsForValues(values, context)
  const resolved = await resolveEnrollment(values, paths, context)
  const shouldSave = values.save === true
    || (resolved.providedByFlags && values['no-save'] !== true)
  if (shouldSave) {
    await writeRuntimeEnrollment(paths.configPath, resolved.enrollment)
    if (context.platform === 'win32') await secureWindowsCredentialFile(paths.configPath, context.runner)
    context.stdout(`saved runtime configuration to ${paths.configPath}`)
  }
  await startRuntimeSession(resolved.enrollment, context)
  return 0
}

async function serviceCommand(args: string[], context: CLIContext): Promise<number> {
  const [action, ...rest] = args
  if (!action || action === 'help' || action === '--help' || action === '-h') {
    context.stdout(serviceUsage())
    return 0
  }
  switch (action) {
    case 'install':
      return installService(rest, context)
    case 'start':
    case 'stop':
    case 'restart':
      return controlService(action, rest, context)
    case 'status':
      return statusService(rest, context)
    case 'uninstall':
      return uninstallService(rest, context)
    default:
      throw new Error(`unknown service command: ${action}`)
  }
}

async function installService(args: string[], context: CLIContext): Promise<number> {
  const { values } = parseArgs({
    args,
    strict: true,
    allowPositionals: false,
    options: {
      ...connectionOptions,
      replace: { type: 'boolean' },
      'no-start': { type: 'boolean' },
    },
  })
  if (values.help) {
    context.stdout(serviceInstallUsage())
    return 0
  }
  const paths = pathsForValues(values, context)
  const existing = await readRuntimeEnrollmentIfExists(paths.configPath, context.home)
  const resolved = await resolveEnrollment(values, paths, context)
  if (existing && resolved.provided && !sameEnrollment(existing, resolved.enrollment) && values.replace !== true) {
    const identity = existing.runtimeId ? `runtime ${existing.runtimeId}` : existing.serverUrl
    throw new Error(`this account is already enrolled with ${identity}; pass --replace to replace it`)
  }
  const enrollment = existing && sameEnrollment(existing, resolved.enrollment)
    ? {
        ...resolved.enrollment,
        runtimeId: resolved.enrollment.runtimeId ?? existing.runtimeId,
        teamId: resolved.enrollment.teamId ?? existing.teamId,
      }
    : resolved.enrollment
  const manager = serviceManager(context, paths)

  // Stage first so a damaged npm/npx installation cannot replace a valid
  // enrollment and then fail before it has an executable to run.
  const staged = await stageRuntimeArtifacts(paths, runtimeClientVersion, {
    entryPath: context.entryPath,
    protoPath: context.protoPath,
  }, context.platform)
  await writeRuntimeEnrollment(paths.configPath, enrollment)

  const spec = runtimeServiceSpec({
    paths,
    entryPath: staged.launcherPath,
    nodePath: context.nodePath,
    environmentPath: context.env.PATH,
    platform: context.platform,
  })
  await manager.install(spec, { start: values['no-start'] !== true })
  await recordRuntimeServiceInstall({
    paths,
    version: runtimeClientVersion,
    backend: manager.backend,
    entryPath: staged.entryPath,
    nodePath: context.nodePath,
  })
  context.stdout(values['no-start']
    ? `installed Memoh Runtime service (${manager.backend})`
    : `installed and started Memoh Runtime service (${manager.backend})`)
  return 0
}

async function controlService(
  action: 'start' | 'stop' | 'restart',
  args: string[],
  context: CLIContext,
): Promise<number> {
  const { values } = parseArgs({
    args,
    strict: true,
    allowPositionals: false,
    options: { help: { type: 'boolean', short: 'h' } },
  })
  if (values.help) {
    context.stdout(`Usage: memoh-runtime service ${action}`)
    return 0
  }
  const paths = resolveRuntimePaths({ home: context.home, env: context.env })
  await serviceManager(context, paths)[action]()
  const pastTense = action === 'stop' ? 'stopped' : `${action}ed`
  context.stdout(`${pastTense} Memoh Runtime service`)
  return 0
}

async function statusService(args: string[], context: CLIContext): Promise<number> {
  const { values } = parseArgs({
    args,
    strict: true,
    allowPositionals: false,
    options: {
      json: { type: 'boolean' },
      help: { type: 'boolean', short: 'h' },
    },
  })
  if (values.help) {
    context.stdout('Usage: memoh-runtime service status [--json]')
    return 0
  }
  const paths = resolveRuntimePaths({ home: context.home, env: context.env })
  const manager = serviceManager(context, paths)
  const [status, manifest] = await Promise.all([
    manager.status(),
    readInstallManifest(paths.manifestPath),
  ])
  const result = {
    ...status,
    version: manifest?.packageVersion,
    configPath: manifest?.configPath ?? paths.configPath,
    executableReady: manifest ? await pathExists(manifest.entryPath) && await pathExists(manifest.nodePath) : false,
  }
  if (values.json) {
    context.stdout(JSON.stringify(result))
  } else {
    const version = result.version ? `, version ${result.version}` : ''
    const executable = result.executableReady ? '' : ', executable unavailable'
    context.stdout(`Memoh Runtime service: ${result.state} (${result.backend}${version}${executable})`)
  }
  return status.state === 'running' ? 0 : 1
}

async function uninstallService(args: string[], context: CLIContext): Promise<number> {
  const { values } = parseArgs({
    args,
    strict: true,
    allowPositionals: false,
    options: {
      purge: { type: 'boolean' },
      help: { type: 'boolean', short: 'h' },
    },
  })
  if (values.help) {
    context.stdout('Usage: memoh-runtime service uninstall [--purge]')
    return 0
  }
  const paths = resolveRuntimePaths({ home: context.home, env: context.env })
  const manifest = await readInstallManifest(paths.manifestPath)
  if (values.purge) assertSafePurge(paths, manifest?.configPath ?? paths.configPath)
  await serviceManager(context, paths).uninstall()
  await removeRuntimeInstallManifest(paths)
  if (values.purge) {
    await rm(manifest?.configPath ?? paths.configPath, { force: true })
    await rm(paths.runtimeHome, { recursive: true, force: true })
  }
  context.stdout(values.purge
    ? 'uninstalled Memoh Runtime service and removed its local configuration'
    : 'uninstalled Memoh Runtime service; local configuration was preserved')
  return 0
}

async function resolveEnrollment(
  values: Record<string, string | boolean | undefined>,
  paths: RuntimePaths,
  context: CLIContext,
): Promise<EnrollmentResolution> {
  const flagServer = stringValue(values.server)
  const flagKey = stringValue(values.key)
  const serverUrl = flagServer ?? nonEmpty(context.env.MEMOH_RUNTIME_SERVER)
  const key = flagKey ?? nonEmpty(context.env.MEMOH_RUNTIME_KEY)
  const provided = serverUrl !== undefined || key !== undefined
  if (provided) {
    if (!serverUrl || !key) {
      throw new Error('--server and --key must be provided together (or set both MEMOH_RUNTIME_SERVER and MEMOH_RUNTIME_KEY)')
    }
    const teamId = stringValue(values['team-id']) ?? nonEmpty(context.env.MEMOH_RUNTIME_TEAM_ID)
    const runtimeId = stringValue(values['runtime-id']) ?? nonEmpty(context.env.MEMOH_RUNTIME_ID)
    const environmentInsecure = parseBooleanEnvironment(
      context.env.MEMOH_RUNTIME_INSECURE_LOCALHOST,
      'MEMOH_RUNTIME_INSECURE_LOCALHOST',
    )
    return {
      enrollment: normalizeRuntimeEnrollment({
        runtimeId,
        serverUrl,
        key,
        teamId,
        insecureLocalhost: values['insecure-localhost'] === true || environmentInsecure === true,
      }, context.home),
      provided: true,
      providedByFlags: flagServer !== undefined || flagKey !== undefined,
    }
  }
  if (values['team-id'] !== undefined || values['runtime-id'] !== undefined || values['insecure-localhost'] === true) {
    throw new Error('--team-id, --runtime-id, and --insecure-localhost require --server and --key')
  }
  return {
    enrollment: await readRuntimeEnrollment(paths.configPath, context.home),
    provided: false,
    providedByFlags: false,
  }
}

async function startRuntimeSession(enrollment: RuntimeEnrollment, context: CLIContext): Promise<void> {
  const controller = new AbortController()
  const stop = () => controller.abort()
  process.once('SIGINT', stop)
  process.once('SIGTERM', stop)
  try {
    const session = context.createSession({
      serverUrl: enrollment.serverUrl,
      key: enrollment.key,
      teamId: enrollment.teamId,
      workspaceBase: context.home,
      insecureLocalhost: enrollment.insecureLocalhost,
    }, {
      onStatus: (status, error) => context.stdout(error ? `${status}: ${error}` : status),
      warn: message => context.stderr(message),
    })
    await session.start(controller.signal)
  } finally {
    process.off('SIGINT', stop)
    process.off('SIGTERM', stop)
  }
}

function serviceManager(context: CLIContext, paths: RuntimePaths): RuntimeServiceManager {
  return createRuntimeServiceManager({
    platform: context.platform,
    paths,
    runner: context.runner,
    uid: context.uid,
  })
}

function pathsForValues(
  values: Record<string, string | boolean | undefined>,
  context: CLIContext,
): RuntimePaths {
  const paths = resolveRuntimePaths({ home: context.home, env: context.env })
  const config = stringValue(values.config)
  return config ? { ...paths, configPath: resolve(config) } : paths
}

async function pathExists(path: string): Promise<boolean> {
  try {
    await access(path)
    return true
  } catch {
    return false
  }
}

function assertSafePurge(paths: RuntimePaths, configPath: string): void {
  const defaultRuntimeHome = resolve(paths.home, '.memoh', 'runtime')
  const defaultConfigPath = resolve(paths.home, '.memoh', 'runtime.json')
  if (resolve(paths.runtimeHome) !== defaultRuntimeHome || resolve(configPath) !== defaultConfigPath) {
    throw new Error('--purge only removes the default Memoh Runtime directory; remove custom paths manually')
  }
}

function stringValue(value: string | boolean | undefined): string | undefined {
  return typeof value === 'string' ? value : undefined
}

function nonEmpty(value: string | undefined): string | undefined {
  return value?.trim() || undefined
}

function rootUsage(): string {
  return `Usage: memoh-runtime <command> [options]

Commands:
  run       Run the Remote Runtime in the foreground (default)
  service   Install and manage the background service
  version   Print the Runtime version

Run "memoh-runtime <command> --help" for command-specific help.`
}

function runUsage(): string {
  return `Usage: memoh-runtime run [--server <url> --key <key>] [options]

Options:
  --team-id <uuid>          Team routing context
  --runtime-id <uuid>       Local enrollment identity
  --insecure-localhost      Allow ws:// for localhost development
  --config <path>           Override the saved configuration path
  --save                    Persist environment-provided credentials
  --no-save                 Do not persist flag-provided credentials

With no connection flags or environment variables, run loads the saved configuration.`
}

function serviceUsage(): string {
  return `Usage: memoh-runtime service <command>

Commands:
  install     Save enrollment and install the background service
  start       Start the installed service
  stop        Stop the installed service
  restart     Restart the installed service
  status      Show the installed service state
  uninstall   Remove the service (configuration is preserved by default)`
}

function serviceInstallUsage(): string {
  return `Usage: memoh-runtime service install [--server <url> --key <key>] [options]

Options:
  --runtime-id <uuid>       Local enrollment identity
  --team-id <uuid>          Team routing context
  --insecure-localhost      Allow ws:// for localhost development
  --config <path>           Override the saved configuration path
  --replace                 Replace a different saved enrollment
  --no-start                Install without starting immediately`
}

export function formatCLIError(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}
