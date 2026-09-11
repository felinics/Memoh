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
  // systemd expands dollars in argv, but the executable path is resolved separately.
  return `[Unit]
Description=Memoh Runtime

[Service]
Type=simple
Environment=${systemdQuote(`PATH=${spec.servicePath}`, false)}
ExecStart=${systemdQuote(spec.nodePath, false)} ${systemdQuote(spec.entryPath)} run --config ${systemdQuote(spec.configPath)}
WorkingDirectory=${systemdPath(spec.workingDirectory)}
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
    async register(spec) {
      await writeFileAtomic(paths.systemdUnitPath, renderSystemdUnit(spec), 0o600)
      await requireCommand(runner, 'systemd-analyze', ['--user', 'verify', paths.systemdUnitPath])
      await run(['daemon-reload'])
      await run(['enable', paths.systemdUnitPath])
    },
    async start() {
      await run(['start', unitName])
    },
    async stop() {
      await run(['stop', unitName], [0, 5])
    },
    async status(): Promise<RuntimeServiceStatus> {
      const result = await runner(systemctl, [...baseArgs, 'show', unitName, '--property=LoadState,ActiveState,SubState,MainPID'])
      const fields = Object.fromEntries(result.stdout.trim().split('\n').map(line => line.split('=')))
      if (fields.LoadState === 'not-found') return { backend: 'systemd-user', state: 'not-installed' }
      if (result.code !== 0 || fields.LoadState !== 'loaded') {
        return { backend: 'systemd-user', state: 'unknown', detail: result.stderr.trim() || fields.LoadState }
      }
      const pid = Number(fields.MainPID)
      return {
        backend: 'systemd-user',
        state: fields.ActiveState === 'active' && fields.SubState === 'running' && pid > 0 ? 'running'
          : ['inactive', 'failed'].includes(fields.ActiveState) ? 'stopped' : 'unknown',
        pid: pid > 0 ? pid : undefined,
        detail: fields.SubState,
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

function systemdQuote(value: string, expandDollar = true): string {
  if (expandDollar) value = value.replaceAll('$', () => '$$')
  if (value.includes('\0') || value.includes('\n') || value.includes('\r')) {
    throw new Error('systemd service value contains unsupported control characters')
  }
  return `"${value.replaceAll('\\', '\\\\').replaceAll('"', '\\"').replaceAll('%', '%%')}"`
}

// Path-valued directives do not use ExecStart's argument unquoting rules.
function systemdPath(value: string): string {
  if (!value.startsWith('/') || value.trim() !== value || /[\\\0\n\r]/.test(value)) throw new Error('invalid systemd working directory')
  return value.replaceAll('%', '%%')
}
