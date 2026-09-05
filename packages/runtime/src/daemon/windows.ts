import { rm } from 'node:fs/promises'

import { writeFileAtomic, type RuntimePaths } from '../runtime-config'
import {
  requireCommand,
  type CommandRunner,
  type RuntimeServiceManager,
  type RuntimeServiceSpec,
  type RuntimeServiceStatus,
} from './types'

const taskName = '\\Memoh\\Runtime'

export function renderWindowsTaskXML(spec: RuntimeServiceSpec, userId: string): string {
  const argumentsValue = [
    windowsQuoteArgument(spec.entryPath),
    'run',
    '--config',
    windowsQuoteArgument(spec.configPath),
  ].join(' ')
  return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Memoh Runtime</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>${xmlEscape(userId)}</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>${xmlEscape(userId)}</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>255</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>${xmlEscape(spec.nodePath)}</Command>
      <Arguments>${xmlEscape(argumentsValue)}</Arguments>
      <WorkingDirectory>${xmlEscape(spec.workingDirectory)}</WorkingDirectory>
    </Exec>
  </Actions>
</Task>
`
}

export function createWindowsTaskServiceManager(paths: RuntimePaths, runner: CommandRunner): RuntimeServiceManager {
  const schtasks = 'schtasks.exe'
  const run = (args: string[], allowedExitCodes?: number[]) => (
    requireCommand(runner, schtasks, args, { allowedExitCodes })
  )
  return {
    backend: 'windows-task-scheduler',
    async install(spec, options = {}) {
      const userId = await currentWindowsUserID(runner)
      await secureWindowsCredentialFile(spec.configPath, runner, userId)
      // Task Scheduler's XML importer recognizes UTF-16, but it also accepts a
      // UTF-8 file whose declaration names UTF-16 inconsistently on some older
      // builds. Write the actual UTF-16LE payload with a BOM for deterministic
      // behavior across supported Windows versions.
      const xml = renderWindowsTaskXML(spec, userId)
      const utf16 = Buffer.concat([Buffer.from([0xff, 0xfe]), Buffer.from(xml, 'utf16le')])
      await writeFileAtomic(paths.windowsTaskXMLPath, utf16, 0o600)
      await run(['/create', '/tn', taskName, '/xml', paths.windowsTaskXMLPath, '/f'])
      if (options.start !== false) await run(['/run', '/tn', taskName])
    },
    async start() {
      await run(['/run', '/tn', taskName])
    },
    async stop() {
      await run(['/end', '/tn', taskName], [0, 1])
    },
    async restart() {
      await run(['/end', '/tn', taskName], [0, 1])
      await run(['/run', '/tn', taskName])
    },
    async status(): Promise<RuntimeServiceStatus> {
      const result = await runner('powershell.exe', [
        '-NoProfile',
        '-NonInteractive',
        '-Command',
        '(Get-ScheduledTask -TaskPath \'\\Memoh\\\' -TaskName \'Runtime\' -ErrorAction Stop).State.ToString()',
      ])
      if (result.code !== 0) {
        const fallback = await runner(schtasks, ['/query', '/tn', taskName])
        return {
          backend: 'windows-task-scheduler',
          state: fallback.code === 0 ? 'unknown' : 'not-installed',
          detail: fallback.code === 0 ? 'Task state could not be read through PowerShell' : undefined,
        }
      }
      const normalized = result.stdout.toLowerCase()
      return {
        backend: 'windows-task-scheduler',
        state: normalized.includes('running') ? 'running' : 'stopped',
      }
    },
    async uninstall() {
      await run(['/end', '/tn', taskName], [0, 1])
      await run(['/delete', '/tn', taskName, '/f'], [0, 1])
      await rm(paths.windowsTaskXMLPath, { force: true })
    },
  }
}

export async function secureWindowsCredentialFile(
  path: string,
  runner: CommandRunner,
  knownUserId?: string,
): Promise<void> {
  const userId = knownUserId ?? await currentWindowsUserID(runner)
  const aclIdentity = /^S-1-/i.test(userId) ? `*${userId}` : userId
  await requireCommand(runner, 'icacls.exe', [
    path,
    '/inheritance:r',
    '/grant:r',
    `${aclIdentity}:(F)`,
  ])
}

async function currentWindowsUserID(runner: CommandRunner): Promise<string> {
  const result = await requireCommand(runner, 'whoami.exe', ['/user', '/fo', 'csv', '/nh'])
  const fields = [...result.stdout.matchAll(/"([^"]*)"/g)].map(match => match[1])
  const sid = fields.find(field => /^S-1-/i.test(field))
  if (sid) return sid
  const account = fields[0]?.trim()
  if (account) return account
  throw new Error('could not determine the current Windows account')
}

function windowsQuoteArgument(value: string): string {
  if (value.includes('\0') || value.includes('"')) {
    throw new Error('Windows service path contains unsupported characters')
  }
  return `"${value}"`
}

function xmlEscape(value: string): string {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll('\'', '&apos;')
}
