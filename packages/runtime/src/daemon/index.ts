import { readFile, rm, symlink } from 'node:fs/promises'
import { join } from 'node:path'

import {
  ensureDirectory,
  writeFileAtomic,
  writeInstallManifest,
  type RuntimeInstallManifest,
  type RuntimePaths,
} from '../runtime-config'
import { createLaunchdServiceManager } from './launchd'
import { createSystemdServiceManager } from './systemd'
import {
  serviceExecutablePath,
  type CommandRunner,
  type RuntimeServiceManager,
  type RuntimeServiceSpec,
} from './types'
import { createWindowsTaskServiceManager } from './windows'

export * from './types'
export { renderLaunchdPlist } from './launchd'
export { renderSystemdUnit } from './systemd'
export { renderWindowsTaskXML, secureWindowsCredentialFile } from './windows'

export interface RuntimeArtifactSources {
  entryPath: string
  protoPath: string
}

export interface StagedRuntimeArtifacts {
  entryPath: string
  protoPath: string
  // What the service manager launches. macOS lists a LaunchAgent in Login
  // Items under its executable's file name, so on POSIX this is a
  // "Memoh Runtime" symlink to the entry rather than "cli.mjs".
  launcherPath: string
}

export const runtimeLauncherName = 'Memoh Runtime'

export async function stageRuntimeArtifacts(
  paths: RuntimePaths,
  version: string,
  sources: RuntimeArtifactSources,
  platform: NodeJS.Platform = process.platform,
): Promise<StagedRuntimeArtifacts> {
  const versionDirectory = join(paths.versionsDir, version)
  await ensureDirectory(versionDirectory)
  const entryPath = join(versionDirectory, 'cli.mjs')
  const protoPath = join(versionDirectory, 'bridge.proto')
  await copyReplacing(sources.entryPath, entryPath, 0o700)
  await copyReplacing(sources.protoPath, protoPath, 0o600)
  if (platform === 'win32') return { entryPath, protoPath, launcherPath: entryPath }
  const launcherPath = join(versionDirectory, runtimeLauncherName)
  await rm(launcherPath, { force: true })
  await symlink('cli.mjs', launcherPath)
  return { entryPath, protoPath, launcherPath }
}

export function createRuntimeServiceManager(options: {
  platform: NodeJS.Platform
  paths: RuntimePaths
  runner: CommandRunner
  uid?: number
}): RuntimeServiceManager {
  switch (options.platform) {
    case 'darwin': {
      if (options.uid === undefined) throw new Error('launchd service installation requires a user ID')
      return createLaunchdServiceManager(options.paths, options.runner, options.uid)
    }
    case 'linux':
      return createSystemdServiceManager(options.paths, options.runner)
    case 'win32':
      return createWindowsTaskServiceManager(options.paths, options.runner)
    default:
      throw new Error(`background service installation is not supported on ${options.platform}`)
  }
}

export function runtimeServiceSpec(options: {
  paths: RuntimePaths
  entryPath: string
  nodePath: string
  environmentPath?: string
  platform?: NodeJS.Platform
}): RuntimeServiceSpec {
  return {
    entryPath: options.entryPath,
    configPath: options.paths.configPath,
    nodePath: options.nodePath,
    runtimeHome: options.paths.runtimeHome,
    logsDir: options.paths.logsDir,
    workingDirectory: options.paths.home,
    servicePath: serviceExecutablePath(options.nodePath, options.environmentPath, options.platform),
  }
}

export async function recordRuntimeServiceInstall(options: {
  paths: RuntimePaths
  version: string
  backend: string
  entryPath: string
  nodePath: string
}): Promise<RuntimeInstallManifest> {
  const manifest: RuntimeInstallManifest = {
    schemaVersion: 1,
    packageVersion: options.version,
    backend: options.backend,
    entryPath: options.entryPath,
    configPath: options.paths.configPath,
    nodePath: options.nodePath,
    installedAt: new Date().toISOString(),
  }
  await writeInstallManifest(options.paths.manifestPath, manifest)
  return manifest
}

export async function removeRuntimeInstallManifest(paths: RuntimePaths): Promise<void> {
  await rm(paths.manifestPath, { force: true })
}

async function copyReplacing(source: string, destination: string, mode: number): Promise<void> {
  const content = await readFile(source)
  await writeFileAtomic(destination, content, mode)
}
