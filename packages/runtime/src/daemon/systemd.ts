import { rm } from 'node:fs/promises'

import { nodeErrorCode, writeFileAtomic, type RuntimePaths } from '../runtime-config'
import {
  requireCommand,
  type CommandRunner,
  type RuntimeServiceManager,
  type RuntimeServiceSpec,
  type RuntimeServiceStatus,
} from './types'

const unitName = 'memoh-runtime.service'

export function renderSystemdUnit(spec: RuntimeServiceSpec): string {
  return `[Unit]
Description=Memoh Runtime

[Service]
Type=simple
Environment=${systemdQuote(`PATH=${spec.servicePath}`)}
ExecStart=${systemdQuote(spec.entryPath)} run --config ${systemdQuote(spec.configPath)}
WorkingDirectory=${systemdQuote(spec.workingDirectory)}
UMask=0077
Restart=always
RestartSec=5
TimeoutStopSec=30

[Install]
WantedBy=default.target
`
}

export function createSystemdServiceManager(paths: RuntimePaths, runner: CommandRunner): RuntimeServiceManager {
  const systemctl = 'systemctl'
  const baseArgs = ['--user']
  const run = (args: string[], allowedExitCodes?: number[]) => (
    requireCommand(runner, systemctl, [...baseArgs, ...args], { allowedExitCodes })
  )
  return {
    backend: 'systemd-user',
    async install(spec, options = {}) {
      await writeFileAtomic(paths.systemdUnitPath, renderSystemdUnit(spec), 0o600)
      await run(['daemon-reload'])
      await run(options.start === false ? ['enable', unitName] : ['enable', '--now', unitName])
    },
    async start() {
      await run(['start', unitName])
    },
    async stop() {
      await run(['stop', unitName], [0, 5])
    },
    async restart() {
      await run(['restart', unitName])
    },
    async status(): Promise<RuntimeServiceStatus> {
      const result = await runner(systemctl, [...baseArgs, 'is-active', unitName])
      if (result.code === 0 && result.stdout.trim() === 'active') {
        return { backend: 'systemd-user', state: 'running' }
      }
      try {
        const enabled = await runner(systemctl, [...baseArgs, 'is-enabled', unitName])
        if (enabled.code !== 0 && result.code === 4) {
          return { backend: 'systemd-user', state: 'not-installed' }
        }
      } catch {
        // is-active already provides the best available local state.
      }
      return {
        backend: 'systemd-user',
        state: result.code === 4 ? 'not-installed' : 'stopped',
        detail: result.stderr.trim() || result.stdout.trim() || undefined,
      }
    },
    async uninstall() {
      await run(['disable', '--now', unitName], [0, 1, 5])
      try {
        await rm(paths.systemdUnitPath, { force: true })
      } catch (error) {
        if (nodeErrorCode(error) !== 'ENOENT') throw error
      }
      await run(['daemon-reload'])
      await run(['reset-failed', unitName], [0, 1, 5])
    },
  }
}

function systemdQuote(value: string): string {
  if (value.includes('\0') || value.includes('\n') || value.includes('\r')) {
    throw new Error('systemd service value contains unsupported control characters')
  }
  return `"${value.replaceAll('\\', '\\\\').replaceAll('"', '\\"').replaceAll('%', '%%')}"`
}
