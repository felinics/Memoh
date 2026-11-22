import { detectOsFamily } from '@/lib/runtime-env'

export interface RuntimeCommandCredential {
  key?: string
  team_id?: string
}

export type RuntimeConnectMode = 'connect' | 'replace'

export type RuntimeHostPlatform = 'windows' | 'macos' | 'linux'

// The dialog usually runs on the computer being connected, so the browser's OS
// is the best first guess; the user can still switch. Mobile OSes (Android/iOS)
// cannot run the desktop runtime CLI directly and fall back to 'macos'.
export function detectRuntimeHostPlatform(userAgent?: string): RuntimeHostPlatform {
  const os = userAgent === undefined ? detectOsFamily() : detectOsFamily({ userAgent })
  if (os === 'windows') return 'windows'
  if (os === 'linux') return 'linux'
  return 'macos'
}

export function buildRuntimeConnectCommand(
  serverUrl: string,
  credential: RuntimeCommandCredential | null | undefined,
  mode: RuntimeConnectMode = 'connect',
  platform: RuntimeHostPlatform = 'macos',
): string {
  const key = credential?.key?.trim()
  if (!key) return ''

  const args = [
    'memoh-runtime',
    'enroll',
    '--server',
    serverUrl,
    '--key',
    key,
  ]
  const teamId = credential?.team_id?.trim()
  if (teamId) {
    args.push('--team-id', teamId)
  }
  if (isInsecureLocalhost(serverUrl)) {
    args.push('--insecure-localhost')
  }
  if (mode === 'replace') {
    args.push('--replace')
  }
  // Stop on failure so a rejected enrollment cannot start a different saved connection.
  const chain = [
    'npm install -g @memohai/runtime@latest',
    args.join(' '),
    'memoh-runtime service install',
    'memoh-runtime service start',
  ].join(' && ')
  // Windows PowerShell 5.1 has no `&&`, and its default execution policy blocks
  // the .ps1 shims npm installs. Running the chain in cmd works the same from
  // cmd, Windows PowerShell and PowerShell 7.
  return platform === 'windows' ? `cmd /c "${chain}"` : chain
}

function isInsecureLocalhost(serverUrl: string): boolean {
  const url = new URL(serverUrl)
  const hostname = url.hostname.replace(/^\[|\]$/g, '')
  return url.protocol === 'http:' && ['localhost', '127.0.0.1', '::1'].includes(hostname)
}
