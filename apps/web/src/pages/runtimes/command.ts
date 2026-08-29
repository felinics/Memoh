import packageMetadata from '../../../package.json' with { type: 'json' }

export interface RuntimeCommandCredential {
  key?: string
  team_id?: string
}

// Memoh releases all workspace packages at one version. Deriving this pin
// keeps the generated command on the Runtime artifact built from the same
// source as the Server capability contract.
const runtimePackage = `@memohai/runtime@${packageMetadata.version}`

// The adapter pins must match the versions Desktop bundles
// (apps/desktop/package.json) — the Server capability contract assumes one
// adapter version per release. command.test.ts cross-asserts them.
export const pinnedACPAdapterVersions = Object.freeze({
  '@agentclientprotocol/codex-acp': '1.2.0',
  '@agentclientprotocol/claude-agent-acp': '0.66.0',
})

const codexACPPackage = `@agentclientprotocol/codex-acp@${pinnedACPAdapterVersions['@agentclientprotocol/codex-acp']}`
const claudeAgentACPPackage = `@agentclientprotocol/claude-agent-acp@${pinnedACPAdapterVersions['@agentclientprotocol/claude-agent-acp']}`

export function buildRuntimeConnectCommand(
  serverUrl: string,
  credential: RuntimeCommandCredential | null | undefined,
): string {
  const key = credential?.key?.trim()
  if (!key) return ''

  const runtimeArgs = [
    'memoh-runtime',
    '--server',
    quoteShellWord(serverUrl),
    '--key',
    quoteShellWord(key),
  ]
  const teamId = credential?.team_id?.trim()
  if (teamId) {
    runtimeArgs.push('--team-id', quoteShellWord(teamId))
  }
  if (isInsecureLocalhost(serverUrl)) {
    runtimeArgs.push('--insecure-localhost')
  }

  return [
    'npx',
    '--yes',
    `--package=${runtimePackage}`,
    `--package=${codexACPPackage}`,
    `--package=${claudeAgentACPPackage}`,
    '--',
    ...runtimeArgs,
  ].join(' ')
}

function quoteShellWord(value: string): string {
  const quote = '\''
  const escapedQuote = `${quote}\\${quote}${quote}`
  return `${quote}${value.replaceAll(quote, escapedQuote)}${quote}`
}

function isInsecureLocalhost(serverUrl: string): boolean {
  const url = new URL(serverUrl)
  const hostname = url.hostname.replace(/^\[|\]$/g, '')
  return url.protocol === 'http:' && ['localhost', '127.0.0.1', '::1'].includes(hostname)
}
