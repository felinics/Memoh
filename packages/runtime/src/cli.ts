#!/usr/bin/env node

import { homedir } from 'node:os'

import { RuntimeSession } from './session'

async function main(args: string[]): Promise<void> {
  if (args.includes('--help') || args.includes('-h')) {
    usage(0)
  }
  if (args.includes('--workspace-base')) {
    throw new Error('--workspace-base is no longer supported; Remote Runtime uses the current user home directory')
  }
  const serverUrl = valueAfter(args, '--server') ?? process.env.MEMOH_RUNTIME_SERVER
  const key = valueAfter(args, '--key') ?? process.env.MEMOH_RUNTIME_KEY
  const teamId = (valueAfter(args, '--team-id') ?? process.env.MEMOH_RUNTIME_TEAM_ID)?.trim() || undefined
  if (!serverUrl || !key) {
    throw new Error('--server and --key are required (or set MEMOH_RUNTIME_SERVER and MEMOH_RUNTIME_KEY)')
  }
  const workspaceBase = homedir()
  const controller = new AbortController()
  const stop = () => controller.abort()
  process.once('SIGINT', stop)
  process.once('SIGTERM', stop)
  try {
    const session = new RuntimeSession({
      serverUrl,
      key,
      teamId,
      workspaceBase,
      insecureLocalhost: args.includes('--insecure-localhost'),
    }, {
      onStatus: (status, error) => console.log(error ? `${status}: ${error}` : status),
      warn: message => console.warn(message),
    })
    await session.start(controller.signal)
  } finally {
    process.off('SIGINT', stop)
    process.off('SIGTERM', stop)
  }
}

function valueAfter(args: string[], name: string): string | undefined {
  const index = args.indexOf(name)
  if (index < 0) {
    return undefined
  }
  const value = args[index + 1]
  if (!value || value.startsWith('--')) {
    throw new Error(`${name} requires a value`)
  }
  return value
}

function usage(exitCode: number): never {
  console.error('Usage: memoh-runtime --server <url> --key <key> [--team-id <uuid>] [--insecure-localhost]')
  process.exit(exitCode)
}

main(process.argv.slice(2)).catch((error: unknown) => {
  console.error(error instanceof Error ? error.message : String(error))
  process.exit(1)
})
