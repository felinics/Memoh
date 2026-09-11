import { rm } from 'node:fs/promises'
import { windowsPowerShellEnv } from '../windows-powershell'

import { ensureDirectory, writeFileAtomic, type RuntimePaths } from '../runtime-config'
import {
  requireCommand,
  type CommandRunner,
  type RuntimeServiceManager,
  type RuntimeServiceSpec,
  type RuntimeServiceStatus,
} from './types'

// Task Scheduler names are machine-wide; isolate each account by its SID.

export function renderWindowsTaskXML(spec: RuntimeServiceSpec, userId: string): string {
  // Task Scheduler captures no output, so the process keeps its own log.
  const argumentsValue = [
    windowsQuoteArgument(spec.entryPath),
    'run',
    '--config',
    windowsQuoteArgument(spec.configPath),
    '--log',
    windowsQuoteArgument(`${spec.logsDir}\\runtime.log`),
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
  let identity: Promise<string> | undefined
  const userID = () => identity ??= currentWindowsUserID(runner)
  const taskName = async () => `\\Memoh\\Runtime-${await userID()}`
  const inspect = async (): Promise<string> => {
    const sid = await userID()
    const result = await requireCommand(runner, 'powershell.exe', [
      '-NoProfile', '-NonInteractive', '-Command',
      `$ErrorActionPreference = 'Stop'; $task = Get-ScheduledTask | Where-Object { $_.TaskPath -eq '\\Memoh\\' -and $_.TaskName -eq 'Runtime-${sid}' }; if ($null -eq $task) { 'not-installed' } else { $owner = $task.Principal.UserId; if ($owner -notmatch '^S-1-') { $owner = ([System.Security.Principal.NTAccount]::new($owner)).Translate([System.Security.Principal.SecurityIdentifier]).Value }; if ($owner -ne '${sid}') { throw 'Runtime task belongs to another account' }; $task.State.ToString() }`,
    ], { env: windowsPowerShellEnv() })
    const state = result.stdout.trim().toLowerCase()
    if (!['not-installed', 'running', 'ready', 'disabled', 'queued', 'unknown'].includes(state)) {
      throw new Error('could not determine the Windows runtime task state')
    }
    return state
  }
  const run = async (args: string[], allowedExitCodes?: number[]) => (
    requireCommand(runner, schtasks, [...args, '/tn', await taskName()], { allowedExitCodes })
  )
  const stop = async () => {
    if (['running', 'queued', 'unknown'].includes(await inspect())) await run(['/end'])
  }
  const start = async () => {
    if (await inspect() === 'not-installed') throw new Error('Windows runtime task is not installed')
    await run(['/run'])
  }
  return {
    backend: 'windows-task-scheduler',
    async register(spec) {
      const userId = await userID()
      await inspect()
      await ensureDirectory(spec.logsDir)
      // Write actual UTF-16LE with a BOM for the Task Scheduler XML importer.
      const xml = renderWindowsTaskXML(spec, userId)
      const utf16 = Buffer.concat([Buffer.from([0xff, 0xfe]), Buffer.from(xml, 'utf16le')])
      await writeFileAtomic(paths.windowsTaskXMLPath, utf16, 0o600)
      await run(['/create', '/xml', paths.windowsTaskXMLPath, '/f'])
    },
    start,
    stop,
    async status(): Promise<RuntimeServiceStatus> {
      try {
        const state = await inspect()
        return {
          backend: 'windows-task-scheduler',
          state: state === 'running' ? 'running' : state === 'not-installed' ? 'not-installed'
            : ['ready', 'disabled'].includes(state) ? 'stopped' : 'unknown',
        }
      } catch {
        return { backend: 'windows-task-scheduler', state: 'unknown' }
      }
    },
    async uninstall() {
      if (await inspect() !== 'not-installed') {
        await stop()
        await run(['/delete', '/f'])
      }
      await rm(paths.windowsTaskXMLPath, { force: true })
    },
  }
}

async function currentWindowsUserID(runner: CommandRunner): Promise<string> {
  const result = await requireCommand(runner, 'whoami.exe', ['/user', '/fo', 'csv', '/nh'])
  const fields = [...result.stdout.matchAll(/"([^"]*)"/g)].map(match => match[1])
  const sid = fields.find(field => /^S-1-(?:[0-9]+-)*[0-9]+$/i.test(field))
  if (sid) return sid
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
