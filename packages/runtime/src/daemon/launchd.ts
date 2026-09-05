import { access, mkdir, rm } from 'node:fs/promises'
import { setTimeout as sleep } from 'node:timers/promises'

import { writeFileAtomic, type RuntimePaths } from '../runtime-config'
import {
  requireCommand,
  type CommandRunner,
  type RuntimeServiceManager,
  type RuntimeServiceSpec,
  type RuntimeServiceStatus,
} from './types'

const launchdLabel = 'ai.memoh.runtime'
const bootstrapAttempts = 5
const defaultBootstrapRetryDelayMs = 300
const bootoutWaitAttempts = 50

export function renderLaunchdPlist(spec: RuntimeServiceSpec): string {
  return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>${xmlEscape(launchdLabel)}</string>
  <key>ProgramArguments</key>
  <array>
    <string>${xmlEscape(spec.entryPath)}</string>
    <string>run</string>
    <string>--config</string>
    <string>${xmlEscape(spec.configPath)}</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>${xmlEscape(spec.servicePath)}</string>
  </dict>
  <key>WorkingDirectory</key>
  <string>${xmlEscape(spec.workingDirectory)}</string>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ThrottleInterval</key>
  <integer>5</integer>
  <key>StandardOutPath</key>
  <string>${xmlEscape(`${spec.logsDir}/runtime.log`)}</string>
  <key>StandardErrorPath</key>
  <string>${xmlEscape(`${spec.logsDir}/runtime-error.log`)}</string>
</dict>
</plist>
`
}

export function createLaunchdServiceManager(
  paths: RuntimePaths,
  runner: CommandRunner,
  uid: number,
  options: { bootstrapRetryDelayMs?: number } = {},
): RuntimeServiceManager {
  const launchctl = '/bin/launchctl'
  const domain = `gui/${uid}`
  const target = `${domain}/${launchdLabel}`
  const retryDelayMs = options.bootstrapRetryDelayMs ?? defaultBootstrapRetryDelayMs
  const loaded = async () => (await runner(launchctl, ['print', target])).code === 0
  // `launchctl bootout` returns before launchd has finished tearing the job
  // down, so a bootstrap issued right after it intermittently fails with
  // "Bootstrap failed: 5: Input/output error". Retrying briefly is enough.
  const bootstrap = async () => {
    for (let attempt = 1; ; attempt++) {
      try {
        await requireCommand(runner, launchctl, ['bootstrap', domain, paths.launchdPlistPath])
        return
      } catch (error) {
        if (attempt >= bootstrapAttempts) throw error
        await sleep(retryDelayMs)
      }
    }
  }
  const bootstrapIfUnloaded = async () => {
    if (await loaded()) return
    await bootstrap()
  }
  // bootout is asynchronous as well: launchctl print keeps reporting the job
  // for a moment, which would make a status check right after stop or
  // uninstall claim the service is still running.
  const bootout = async () => {
    await requireCommand(runner, launchctl, ['bootout', target], { allowedExitCodes: [0, 3] })
    for (let attempt = 0; attempt < bootoutWaitAttempts && await loaded(); attempt++) {
      await sleep(retryDelayMs / 3)
    }
  }
  return {
    backend: 'launchd-user',
    async install(spec, options = {}) {
      await mkdir(spec.logsDir, { recursive: true, mode: 0o700 })
      await writeFileAtomic(paths.launchdPlistPath, renderLaunchdPlist(spec), 0o600)
      if (await loaded()) await bootout()
      if (options.start !== false) await bootstrap()
    },
    async start() {
      await bootstrapIfUnloaded()
    },
    async stop() {
      await bootout()
    },
    async restart() {
      if (await loaded()) {
        await requireCommand(runner, launchctl, ['kickstart', '-k', target])
        return
      }
      await bootstrapIfUnloaded()
    },
    async status(): Promise<RuntimeServiceStatus> {
      const result = await runner(launchctl, ['print', target])
      if (result.code === 0) {
        return {
          backend: 'launchd-user',
          state: /\bstate\s*=\s*running\b/.test(result.stdout) ? 'running' : 'stopped',
        }
      }
      try {
        await access(paths.launchdPlistPath)
        return { backend: 'launchd-user', state: 'stopped' }
      } catch {
        return { backend: 'launchd-user', state: 'not-installed' }
      }
    },
    async uninstall() {
      await bootout()
      await rm(paths.launchdPlistPath, { force: true })
    },
  }
}

function xmlEscape(value: string): string {
  if (value.includes('\0')) throw new Error('launchd service value contains a null byte')
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll('\'', '&apos;')
}
