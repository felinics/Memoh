import { spawn, spawnSync } from 'node:child_process'
import { dirname, extname } from 'node:path'
import { fileURLToPath } from 'node:url'

const frontendExtensions = new Set(['.vue', '.js', '.jsx', '.mjs', '.cjs', '.ts', '.tsx'])
// These files are read by internal/handlers display and contract tests.
const handlerInputs = new Set([
  'docker/Dockerfile.workspace', 'spec/swagger.json',
  'scripts/desktop-install.sh', 'scripts/desktop-style.sh',
  'scripts/display-apply-style.sh', 'scripts/display-prepare.sh',
])
const goGlobals = new Set(['go.mod', 'go.sum', 'go.work', 'go.work.sum', '.golangci.yml', '.golangci.yaml', 'mise.toml'])

// Both inputs describe the index: unstaged edits must not decide what a commit checks.
export function planChecks(changed, tracked, full = false) {
  const packages = new Set(tracked.filter(path => path.endsWith('.go')).map(dirname))
  const go = new Set()
  let allGo = full
  let allWeb = full
  let web = full
  for (const path of changed) {
    if (goGlobals.has(path) || path.endsWith('.sql') || path.startsWith('conf/')) allGo = true
    if (handlerInputs.has(path)) go.add('./internal/handlers')
    if (path === 'packages/ui' || path === 'mise.toml' || /(^|\/)(package\.json|pnpm-lock\.yaml|pnpm-workspace\.yaml|eslint\.config\.[cm]?js|tsconfig[^/]*\.json)$/.test(path)) {
      allWeb = true
      web = true
    }
    if (frontendExtensions.has(extname(path))) web = true
    // Resources (prompts, fixtures, templates, wasm, etc.) belong to their nearest
    // Go package too. Only checking .go files silently misses embedded inputs.
    const backend = /^(cmd|internal|db|templates)\//.test(path)
    if (path.endsWith('.go') || backend) {
      let dir = dirname(path)
      if (path.endsWith('.go') && !packages.has(dir)) {
        // Deleting/moving the last source file needs full CI for its former importers.
        allGo = true
        continue
      }
      while (!packages.has(dir) && dir !== '.') dir = dirname(dir)
      if (packages.has(dir)) go.add(dir === '.' ? '.' : `./${dir}`)
      else allGo = true
    }
  }
  return {
    go: full ? ['./...'] : [...go].sort(),
    web: allWeb ? 'full' : web ? 'staged' : 'skip',
    ...(!full && allGo ? { fullGoInCI: true } : {}),
  }
}

function git(args) {
  const result = spawnSync('git', args, { encoding: 'utf8' })
  if (result.status !== 0) throw new Error(result.stderr || `git ${args[0]} failed`)
  return result.stdout
}

function indexEntries() {
  return git(['ls-files', '--stage', '-z']).split('\0').filter(Boolean).map(line => {
    const tab = line.indexOf('\t')
    const [mode, oid, stage] = line.slice(0, tab).split(' ')
    if (stage !== '0') throw new Error('Resolve index conflicts before committing')
    return { mode, oid, path: line.slice(tab + 1) }
  })
}

function checkSizes(changed, entries) {
  const changedSet = new Set(changed)
  const blobs = entries.filter(entry => changedSet.has(entry.path) && entry.mode !== '160000')
  if (!blobs.length) return
  // Inspect staged blobs, not working files; batch avoids one process per file.
  const result = spawnSync('git', ['cat-file', '--batch-check=%(objectsize)'], {
    input: blobs.map(entry => entry.oid).join('\n') + '\n', encoding: 'utf8',
  })
  if (result.status !== 0) throw new Error(result.stderr || 'Cannot inspect staged blob sizes')
  const sizes = result.stdout.trim().split('\n').map(Number)
  if (sizes.length !== blobs.length || sizes.some(size => !Number.isFinite(size))) throw new Error('Invalid staged blob sizes')
  const large = blobs.filter((entry, index) => sizes[index] > 1048576)
  if (large.length) throw new Error(`Staged files exceed 1 MiB: ${large.map(entry => entry.path).join(', ')}`)
}

async function run(label, command, args, timeoutSeconds, capture = false) {
  console.log(`[hooks] ${label}: ${command} ${args.join(' ')}`)
  const start = performance.now()
  const env = command === 'go' || command === 'golangci-lint' ? { ...process.env, GOMAXPROCS: '2' } : process.env
  const child = spawn(command, args, { env, stdio: capture ? ['inherit', 'pipe', 'inherit'] : 'inherit', detached: process.platform !== 'win32' })
  let output = ''
  if (capture) child.stdout.on('data', chunk => { output += chunk })
  let stopped = false
  let killTimer
  const kill = signal => {
    try {
      if (process.platform === 'win32') child.kill(signal)
      else process.kill(-child.pid, signal)
    }
    catch (error) { if (error.code !== 'ESRCH') console.error(`[hooks] cleanup: ${error.message}`) }
  }
  const stop = () => {
    stopped = true
    kill('SIGTERM')
    killTimer ??= setTimeout(() => kill('SIGKILL'), 1000)
  }
  const timer = setTimeout(() => {
    console.error(`[hooks] ${label} exceeded ${timeoutSeconds}s; check failed (not skipped)`)
    console.error('[hooks] The wall-clock budget includes tool startup and compilation; a timeout does not establish a test failure.')
    console.error('[hooks] For an expected cold build, retry with a larger MEMOH_CHECK_TIMEOUT_SECONDS value (see .husky/README.md).')
    stop()
  }, timeoutSeconds * 1000)
  process.once('SIGINT', stop)
  process.once('SIGTERM', stop)
  try {
    await new Promise((resolve, reject) => {
      child.once('error', reject)
      child.once('close', code => code === 0 && !stopped ? resolve() : reject(new Error(`${label} failed (${stopped ? 'interrupted/timeout' : code})`)))
    })
    return output
  }
  finally {
    clearTimeout(timer)
    // On cancellation, allow the group cleanup timer to run even if its shell exited.
    process.removeListener('SIGINT', stop)
    process.removeListener('SIGTERM', stop)
    console.log(`[hooks] ${label}: ${((performance.now() - start) / 1000).toFixed(2)}s`)
  }
}

async function main() {
  // Git paths and tool arguments must share the worktree root, even for manual calls.
  process.chdir(git(['rev-parse', '--show-toplevel']).trim())
  const only = process.argv[2]
  if (only && !['--plan', 'sizes', 'web', 'go', 'go-test'].includes(only)) throw new Error(`Unknown check: ${only}`)
  const full = process.env.MEMOH_FULL_CHECKS === '1'
  const timeout = Number(process.env.MEMOH_CHECK_TIMEOUT_SECONDS || (full ? 900 : 180))
  if (!Number.isFinite(timeout) || timeout <= 0) throw new Error('MEMOH_CHECK_TIMEOUT_SECONDS must be positive')
  const changed = git(['diff', '--cached', '--name-only', '--no-renames', '-z']).split('\0').filter(Boolean)
  const entries = indexEntries()
  const plan = planChecks(changed, entries.map(entry => entry.path), full)
  if (only === '--plan') { console.log(JSON.stringify(plan)); return }
  if (!only || only === 'sizes') checkSizes(changed, entries)
  if (only === 'sizes') return
  const started = performance.now()
  if (!only && changed.some(path => path.startsWith('.husky/') || path === 'scripts/hook-checks.test.mjs')) {
    await run('Hook regression tests', process.execPath, ['--test', 'scripts/hook-checks.test.mjs'], timeout)
  }
  if (!only || only === 'web') {
    if (plan.web === 'skip') console.log('[hooks] Web: no relevant staged changes')
    else await run('Web lint', 'pnpm', plan.web === 'full' ? ['exec', 'eslint', '.'] : ['exec', 'lint-staged'], timeout)
  }
  if (!only || only === 'go' || only === 'go-test') {
    if (plan.fullGoInCI) console.log('[hooks] Go: broad impact; full validation belongs to CI or explicit MEMOH_FULL_CHECKS=1')
    if (plan.go.length && !plan.go.includes('./...')) {
      // Let Go apply GOOS/GOARCH/GOFLAGS and build tags. Explicitly naming a
      // tag-only package fails where ./... would skip it. Preserve every other
      // load error so lint/test can report it instead of silently passing.
      // golangci-lint expects filesystem paths, whereas go test also accepts imports.
      const format = '{{if .Error}}{{if eq (printf "%v" .Error.Err) (printf "build constraints exclude all Go files in %s" .Dir)}}{{else}}{{if .Dir}}{{.Dir}}{{else}}{{.ImportPath}}{{end}}{{end}}{{else}}{{if .Dir}}{{.Dir}}{{else}}{{.ImportPath}}{{end}}{{end}}'
      const selected = await run('Go package selection', 'go', ['list', '-e', '-f', format, ...plan.go], timeout, true)
      plan.go = selected.split('\n').map(path => path.trim()).filter(Boolean)
      if (!plan.go.length) console.log('[hooks] Go: selected packages are excluded by current build constraints')
    }
    if (!plan.go.length) console.log(plan.fullGoInCI
      ? '[hooks] Go: no local package checks; full CI must pass before merging'
      : '[hooks] Go: no packages selected for this build context')
    else {
      // Sequential Go checks avoid competing compilation/type-loading workloads.
      if (!only || only === 'go') await run('Go lint', 'golangci-lint', ['run', '--concurrency=2', ...plan.go], timeout)
      if (!only || only === 'go-test') await run('Go test', 'go', ['test', '-p=2', `-timeout=${timeout}s`, ...plan.go], timeout)
    }
  }
  console.log(`[hooks] Total: ${((performance.now() - started) / 1000).toFixed(2)}s`)
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  main().catch(error => { console.error(`[hooks] ${error.message}`); process.exitCode = 1 })
}
