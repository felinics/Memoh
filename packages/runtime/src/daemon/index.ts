import { access, stat } from 'node:fs/promises'
import { constants } from 'node:fs'
import { isAbsolute, join } from 'node:path'
import { setTimeout as sleep } from 'node:timers/promises'

import type { RuntimePaths } from '../runtime-config'
import { createLaunchdServiceManager } from './launchd'
import { createSystemdServiceManager } from './systemd'
import type { CommandRunner, RuntimeServiceManager, RuntimeServiceState } from './types'
import { createWindowsTaskServiceManager } from './windows'

export * from './types'

export function createRuntimeServiceManager(options: {
  platform: NodeJS.Platform
  paths: RuntimePaths
  runner: CommandRunner
  uid?: number
}): RuntimeServiceManager {
  switch (options.platform) {
    case 'darwin':
      if (options.uid === undefined) throw new Error('launchd service installation requires a user ID')
      return createLaunchdServiceManager(options.paths, options.runner, options.uid)
    case 'linux':
      return createSystemdServiceManager(options.paths, options.runner)
    case 'win32':
      return createWindowsTaskServiceManager(options.paths, options.runner)
    default:
      throw new Error(`background services are not supported on ${options.platform}`)
  }
}

// Keep the package manager's entry path, including symlinks. Resolving it to a
// version-specific binary would break the service when that version is removed.
export async function findNodeExecutable(envPath: string | undefined, platform: NodeJS.Platform): Promise<string> {
  for (const directory of envPath?.split(platform === 'win32' ? ';' : ':') ?? []) {
    if (!isAbsolute(directory)) continue
    const path = join(directory, platform === 'win32' ? 'node.exe' : 'node')
    try {
      if (!(await stat(path)).isFile()) continue
      await access(path, platform === 'win32' ? constants.R_OK : constants.X_OK)
      return path
    } catch { /* Try the next PATH entry. */ }
  }
  throw new Error('Node was not found in PATH; install Node and rerun service install')
}

export async function waitForService(manager: RuntimeServiceManager, target: RuntimeServiceState): Promise<void> {
  const deadline = Date.now() + 15_000
  let observations = 0
  do {
    const { state } = await manager.status()
    const matched = state === target || (target === 'stopped' && state === 'not-installed')
    observations = matched ? observations + 1 : 0
    // A process can appear briefly before exiting on a startup error.
    if (observations >= (target === 'running' ? 2 : 1)) return
    await sleep(250)
  } while (Date.now() < deadline)
  throw new Error(`runtime service did not reach ${target}; inspect the service logs`)
}
