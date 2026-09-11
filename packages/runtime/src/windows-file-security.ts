import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { windowsPowerShellEnv } from './windows-powershell'

// Arguments are encoded as data in a PowerShell literal, never a shell command.
async function powershell(script: string): Promise<void> {
  await promisify(execFile)('powershell.exe', ['-NoProfile', '-NonInteractive', '-EncodedCommand', Buffer.from(script, 'utf16le').toString('base64')], { timeout: 30_000, windowsHide: true, env: windowsPowerShellEnv() })
}

export async function checkWindowsAccess(path: string, privateFile: boolean): Promise<void> {
  await powershell(`
$ErrorActionPreference = 'Stop'
$path = '${path.replaceAll('\'', '\'\'')}'
$sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$trusted = @($sid, 'S-1-5-18', 'S-1-5-32-544', 'S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464')
$item = Get-Item -LiteralPath $path -Force
$leaf = $true
while ($null -ne $item) {
  if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Runtime path cannot be a reparse point' }
  $acl = Get-Acl -LiteralPath $item.FullName
  if ($trusted -notcontains $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value) { throw 'Runtime path has an untrusted owner' }
  foreach ($rule in $acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier])) {
    if ($rule.AccessControlType -ne 'Allow' -or $trusted -contains $rule.IdentityReference.Value -or ($rule.PropagationFlags -band [Security.AccessControl.PropagationFlags]::InheritOnly)) { continue }
    $unsafe = [Security.AccessControl.FileSystemRights]::Delete -bor [Security.AccessControl.FileSystemRights]::DeleteSubdirectoriesAndFiles -bor [Security.AccessControl.FileSystemRights]::ChangePermissions -bor [Security.AccessControl.FileSystemRights]::TakeOwnership
    if ($leaf) {
      $unsafe = $unsafe -bor [Security.AccessControl.FileSystemRights]::Write -bor [Security.AccessControl.FileSystemRights]::Delete
      if (${privateFile ? '$true' : '$false'}) { $unsafe = $unsafe -bor [Security.AccessControl.FileSystemRights]::ReadData }
    }
    if ($rule.FileSystemRights -band $unsafe) { throw 'Runtime path grants unsafe access to another account' }
  }
  $leaf = $false
  if ($item -is [IO.FileInfo]) { $item = $item.Directory } else { $item = $item.Parent }
}
`)
}

export async function protectWindowsFile(path: string): Promise<void> {
  await protectWindowsPath(path, false)
}

export async function protectWindowsDirectory(path: string): Promise<void> {
  await protectWindowsPath(path, true)
}

async function protectWindowsPath(path: string, directory: boolean): Promise<void> {
  const inheritance = directory ? '\'ContainerInherit,ObjectInherit\', \'None\', ' : ''
  await powershell(`
$ErrorActionPreference = 'Stop'
$sid = [Security.Principal.WindowsIdentity]::GetCurrent().User
$acl = New-Object Security.AccessControl.${directory ? 'DirectorySecurity' : 'FileSecurity'}
$acl.SetOwner($sid)
$acl.SetAccessRuleProtection($true, $false)
foreach ($identity in @($sid.Value, 'S-1-5-18', 'S-1-5-32-544')) {
  $principal = New-Object Security.Principal.SecurityIdentifier($identity)
  $rule = New-Object Security.AccessControl.FileSystemAccessRule($principal, 'FullControl', ${inheritance}'Allow')
  $acl.AddAccessRule($rule)
}
# The .NET API persists the modified owner/DACL without requesting SACL access.
[IO.${directory ? 'Directory' : 'File'}]::SetAccessControl('${path.replaceAll('\'', '\'\'')}', $acl)
`)
}
