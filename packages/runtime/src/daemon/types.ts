import { spawn } from 'node:child_process'

export type RuntimeServiceState = 'not-installed' | 'running' | 'stopped' | 'unknown'

export interface RuntimeServiceStatus {
  backend: string
  state: RuntimeServiceState
  detail?: string
}

export interface RuntimeServiceSpec {
  entryPath: string
  configPath: string
  nodePath: string
  runtimeHome: string
  logsDir: string
  workingDirectory: string
  servicePath: string
}

export interface RuntimeServiceManager {
  readonly backend: string
  install(spec: RuntimeServiceSpec, options?: { start?: boolean }): Promise<void>
  start(): Promise<void>
  stop(): Promise<void>
  restart(): Promise<void>
  status(): Promise<RuntimeServiceStatus>
  uninstall(): Promise<void>
}

export interface CommandResult {
  code: number
  stdout: string
  stderr: string
}

export type CommandRunner = (
  command: string,
  args: string[],
  options?: { env?: NodeJS.ProcessEnv },
) => Promise<CommandResult>

const maxCommandOutputBytes = 64 * 1024

export const spawnCommand: CommandRunner = (command, args, options = {}) => (
  new Promise<CommandResult>((resolve, reject) => {
    const child = spawn(command, args, {
      env: options.env,
      shell: false,
      stdio: ['ignore', 'pipe', 'pipe'],
      windowsHide: true,
    })
    let stdout = ''
    let stderr = ''
    child.stdout.setEncoding('utf8')
    child.stderr.setEncoding('utf8')
    child.stdout.on('data', (chunk: string) => {
      if (stdout.length < maxCommandOutputBytes) stdout += chunk.slice(0, maxCommandOutputBytes - stdout.length)
    })
    child.stderr.on('data', (chunk: string) => {
      if (stderr.length < maxCommandOutputBytes) stderr += chunk.slice(0, maxCommandOutputBytes - stderr.length)
    })
    child.once('error', reject)
    child.once('close', code => resolve({ code: code ?? 1, stdout, stderr }))
  })
)

export async function requireCommand(
  runner: CommandRunner,
  command: string,
  args: string[],
  options: { allowedExitCodes?: number[], env?: NodeJS.ProcessEnv } = {},
): Promise<CommandResult> {
  const result = await runner(command, args, { env: options.env })
  const allowed = options.allowedExitCodes ?? [0]
  if (!allowed.includes(result.code)) {
    const detail = result.stderr.trim() || result.stdout.trim() || `exit code ${result.code}`
    throw new Error(`${command} ${args.join(' ')} failed: ${detail}`)
  }
  return result
}

export function serviceExecutablePath(
  nodePath: string,
  environmentPath: string | undefined,
  platform: NodeJS.Platform = process.platform,
): string {
  const separator = platform === 'win32' ? ';' : ':'
  const nodeDirectory = nodePath.replace(/[\\/][^\\/]+$/, '')
  const defaults = platform === 'darwin'
    ? ['/opt/homebrew/bin', '/usr/local/bin', '/usr/bin', '/bin', '/usr/sbin', '/sbin']
    : ['/usr/local/bin', '/usr/bin', '/bin']
  const values = [nodeDirectory, ...(environmentPath?.split(separator) ?? []), ...defaults]
    .map(value => value.trim())
    .filter(value => value && !value.includes('\0') && !value.includes('\n') && !value.includes('\r'))
  return [...new Set(values)].join(separator)
}
