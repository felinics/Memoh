import { execFile } from 'node:child_process'
import { access, readFile, readdir, realpath, stat } from 'node:fs/promises'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { promisify } from 'node:util'

const execFileAsync = promisify(execFile)
const expectedAdapters = Object.freeze([
  Object.freeze({
    packageName: '@agentclientprotocol/codex-acp',
    version: '1.2.0',
    versionOutput: '@agentclientprotocol/codex-acp 1.2.0',
  }),
  Object.freeze({
    packageName: '@agentclientprotocol/claude-agent-acp',
    version: '0.66.0',
    versionOutput: '0.66.0',
  }),
])

export async function verifyPackagedACP(resourcesDirectory) {
  const applicationArchive = join(resourcesDirectory, 'app.asar')
  const nodeModules = join(applicationArchive, 'node_modules')
  const nodeVersion = await runNode(['-p', 'process.versions.node'])
  const nodeMajor = Number.parseInt(nodeVersion, 10)
  if (!Number.isInteger(nodeMajor) || nodeMajor < 22) {
    throw new Error(`packaged Electron must provide Node.js 22+; received ${nodeVersion}`)
  }

  for (const adapter of expectedAdapters) {
    const packageRoot = join(nodeModules, ...adapter.packageName.split('/'))
    const packageJson = JSON.parse(await readFile(join(packageRoot, 'package.json'), 'utf8'))
    if (packageJson.version !== adapter.version) {
      throw new Error(`${adapter.packageName} must be exactly ${adapter.version}`)
    }
    const entry = join(packageRoot, 'dist', 'index.js')
    await access(entry)
    const canonicalEntry = await realpath(entry)
    if (!(await stat(canonicalEntry)).isFile()) {
      throw new Error(`${adapter.packageName} entry must be a regular ASAR file`)
    }
    const version = await runNode([entry, '--version'])
    if (version !== adapter.versionOutput) {
      throw new Error(`${adapter.packageName} returned unexpected version ${version}`)
    }
  }

  await assertNoRedistributedNativeAgents(resourcesDirectory)
  process.stdout.write('✓ packaged Remote ACP adapters verified\n')
}

async function runNode(args) {
  const result = await execFileAsync(process.execPath, args, {
    encoding: 'utf8',
    env: childEnvironment(),
    maxBuffer: 16 * 1024,
    timeout: 10_000,
    windowsHide: true,
  })
  return String(result.stdout).trim()
}

function childEnvironment() {
  const environment = {
    PATH: '/usr/bin:/bin:/usr/sbin:/sbin',
    ELECTRON_RUN_AS_NODE: '1',
  }
  for (const name of ['HOME', 'TMPDIR', 'TMP', 'TEMP', 'LANG', 'LC_ALL', 'SystemRoot']) {
    const value = process.env[name]
    if (value && !value.includes('\0')) environment[name] = value
  }
  return environment
}

async function assertNoRedistributedNativeAgents(resourcesDirectory) {
  const forbidden = /(?:^|\/)(?:codex-(?:darwin|linux|win32)-[^/]+|claude-agent-sdk-(?:darwin|linux|win32)-[^/]+)(?:\/|$)/
  const roots = [resourcesDirectory, join(resourcesDirectory, 'app.asar')]
  for (const root of roots) {
    for (const entry of await recursiveEntries(root)) {
      const normalized = entry.replaceAll('\\', '/')
      if (forbidden.test(normalized)) {
        throw new Error(`native agent package must not be redistributed: ${normalized}`)
      }
    }
  }
}

async function recursiveEntries(root) {
  const entries = []
  const queue = [root]
  while (queue.length > 0) {
    const directory = queue.pop()
    let children
    try {
      children = await readdir(directory, { withFileTypes: true })
    } catch (error) {
      if (error?.code === 'ENOENT' || error?.code === 'ENOTDIR') continue
      throw error
    }
    for (const child of children) {
      const path = join(directory, child.name)
      entries.push(path)
      if (child.isDirectory()) queue.push(path)
    }
  }
  return entries
}

const invokedPath = process.argv[1] ? pathToFileURL(process.argv[1]).href : ''
if (import.meta.url === invokedPath) {
  const resourcesDirectory = process.argv[2]
  if (!resourcesDirectory) throw new Error('packaged resources directory is required')
  await verifyPackagedACP(resourcesDirectory)
}
