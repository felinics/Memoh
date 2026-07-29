export interface RuntimeCommandCredential {
  key?: string
  team_id?: string
}

export function buildRuntimeConnectCommand(
  serverUrl: string,
  credential: RuntimeCommandCredential | null | undefined,
): string {
  const key = credential?.key?.trim()
  if (!key) return ''

  const args = [
    'npx',
    '--yes',
    '@memohai/runtime',
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
  return args.join(' ')
}

function isInsecureLocalhost(serverUrl: string): boolean {
  const url = new URL(serverUrl)
  const hostname = url.hostname.replace(/^\[|\]$/g, '')
  return url.protocol === 'http:' && ['localhost', '127.0.0.1', '::1'].includes(hostname)
}
