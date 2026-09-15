export interface RuntimeCommandCredential {
  key?: string
  team_id?: string
}

// Hosted deployments ship their own distribution of the runtime CLI (for
// example a build with the gateway endpoint preconfigured), published under a
// different npm name. The package is therefore a parameter; self-hosted
// installs keep the upstream default.
export const defaultRuntimeNpmPackage = '@memohai/runtime'

// The OS service manager launches the package's bin, so the command installs
// the CLI globally: the runtime README is explicit that an npx cache is not a
// persistent install, and a registered service pointing at one breaks as soon
// as the cache is reaped. Bin names follow the scoped package name by
// convention (@memohai/runtime → memoh-runtime,
// @memohai/cloud-runtime → memoh-cloud-runtime).
export function runtimeCliBin(npmPackage: string = defaultRuntimeNpmPackage): string {
  const match = /^@memohai\/(.+)$/.exec(npmPackage.trim())
  return match?.[1] ? `memoh-${match[1]}` : 'memoh-runtime'
}

export function buildRuntimeConnectCommand(
  serverUrl: string,
  credential: RuntimeCommandCredential | null | undefined,
  npmPackage: string = defaultRuntimeNpmPackage,
): string {
  const key = credential?.key?.trim()
  if (!key) return ''

  const bin = runtimeCliBin(npmPackage)
  // --replace: every wizard session issues a fresh key, so a re-run always
  // differs from the saved enrollment and enroll would refuse without it.
  // Re-binding the machine to the new credential is exactly the intent.
  const enrollArgs = [bin, 'enroll', '--server', serverUrl, '--key', key]
  const teamId = credential?.team_id?.trim()
  if (teamId) {
    enrollArgs.push('--team-id', teamId)
  }
  if (isInsecureLocalhost(serverUrl)) {
    enrollArgs.push('--insecure-localhost')
  }
  enrollArgs.push('--replace')
  // One command per line, not a `&&` chain: pasting the block runs the lines
  // sequentially in bash/zsh/cmd, and PowerShell 5.1 (the default Windows
  // shell) rejects `&&` outright.
  return [
    `npm install -g ${npmPackage}`,
    enrollArgs.join(' '),
    `${bin} service install`,
    `${bin} service start`,
  ].join('\n')
}

function isInsecureLocalhost(serverUrl: string): boolean {
  const url = new URL(serverUrl)
  const hostname = url.hostname.replace(/^\[|\]$/g, '')
  return url.protocol === 'http:' && ['localhost', '127.0.0.1', '::1'].includes(hostname)
}
