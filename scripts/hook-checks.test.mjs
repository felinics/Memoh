import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { planChecks } from '../.husky/checks.mjs'

const tracked = ['internal/a/a.go', 'internal/b/b.go', 'templates/embed.go', 'root.go']
for (const [name, changed, expected] of [
  ['frontend', ['apps/web/src/a.vue'], { go: [], web: 'staged' }],
  ['docs', ['README.md'], { go: [], web: 'skip' }],
  ['Go package', ['internal/a/a.go'], { go: ['./internal/a'], web: 'skip' }],
  ['deduplicate', ['internal/a/a.go', 'internal/a/a_test.go'], { go: ['./internal/a'], web: 'skip' }],
  ['embedded prompt', ['internal/a/prompts/a.md'], { go: ['./internal/a'], web: 'skip' }],
  ['template', ['templates/workspace/AGENTS.md'], { go: ['./templates'], web: 'skip' }],
  ['mixed', ['internal/b/b.go', 'apps/web/src/a.ts'], { go: ['./internal/b'], web: 'staged' }],
  ['runtime configuration', ['conf/app.toml'], { go: ['./...'], web: 'skip' }],
  ['dependency', ['go.sum'], { go: ['./...'], web: 'skip' }],
  ['SQL inputs', ['db/postgres/queries/b.sql'], { go: ['./...'], web: 'skip' }],
  ['deleted package', ['internal/gone/a.go'], { go: ['./...'], web: 'skip' }],
  ['rename old and new paths', ['internal/gone/a.go', 'internal/a/a.go'], { go: ['./...'], web: 'skip' }],
  ['UI submodule', ['packages/ui'], { go: [], web: 'full' }],
  ['toolchain', ['mise.toml'], { go: ['./...'], web: 'full' }],
  ['frontend configuration', ['eslint.config.mjs'], { go: [], web: 'full' }],
  ['root Go package', ['root.go'], { go: ['.'], web: 'skip' }],
]) {
  test(name, () => assert.deepEqual(planChecks(changed, tracked), expected))
}
test('explicit full checks', () => assert.deepEqual(planChecks([], tracked, true), { go: ['./...'], web: 'full' }))

const runner = fileURLToPath(new URL('../.husky/checks.mjs', import.meta.url))
function fixture(t) {
  const cwd = mkdtempSync(join(tmpdir(), 'memoh-hook-test-'))
  t.after(() => rmSync(cwd, { recursive: true, force: true }))
  // Git hooks export repository-local variables; a fixture must not inherit
  // the caller's index, worktree or object store. Ask Git for the complete list.
  const fixtureEnv = { ...process.env }
  const localNames = spawnSync('git', ['rev-parse', '--local-env-vars'], { encoding: 'utf8' })
  assert.equal(localNames.status, 0, localNames.stderr)
  for (const name of localNames.stdout.trim().split('\n')) delete fixtureEnv[name]
  const git = (...args) => {
    const result = spawnSync('git', args, { cwd, encoding: 'utf8', env: fixtureEnv })
    assert.equal(result.status, 0, result.stderr)
    return result.stdout
  }
  git('init', '-q')
  const bin = join(cwd, 'bin')
  mkdirSync(bin)
  for (const name of ['pnpm', 'go', 'golangci-lint']) {
    writeFileSync(join(bin, name), `#!/usr/bin/env node
console.log('CALLED ${name}', JSON.stringify(process.argv.slice(2)))
if (process.env.FAIL_COMMAND === '${name}') process.exit(7)
if (process.env.BUSY_COMMAND === '${name}') setInterval(() => {}, 1000)
`, { mode: 0o755 })
  }
  const stage = (path, content = '') => {
    mkdirSync(join(cwd, path, '..'), { recursive: true })
    writeFileSync(join(cwd, path), content)
    git('add', '--', path)
  }
  const run = (env = {}, args = []) => spawnSync(process.execPath, [runner, ...args], {
    cwd, encoding: 'utf8', timeout: 10000,
    env: { ...fixtureEnv, MEMOH_FULL_CHECKS: '0', PATH: `${bin}:${process.env.PATH}`, ...env },
  })
  return { cwd, git, stage, run }
}
test('only staged files select checks; spaces are preserved', t => {
  const { cwd, stage, run } = fixture(t)
  stage('notes with spaces.md', 'hello')
  writeFileSync(join(cwd, 'unstaged.go'), 'package example')
  const result = run()
  assert.equal(result.status, 0, result.stderr)
  assert.doesNotMatch(result.stdout, /CALLED/)
})
test('file size uses the index, not a smaller working copy', t => {
  const { cwd, stage, run } = fixture(t)
  stage('large file.md', 'x'.repeat(1048577))
  writeFileSync(join(cwd, 'large file.md'), 'small')
  const result = run()
  assert.equal(result.status, 1)
  assert.match(result.stderr, /Staged files exceed/)
  assert.doesNotMatch(result.stdout, /CALLED/)
})
test('frontend calls lint-staged without Go', t => {
  const { stage, run } = fixture(t)
  stage('apps/web/a.ts', 'export {}')
  const result = run()
  assert.equal(result.status, 0, result.stderr)
  assert.match(result.stdout, /CALLED pnpm \["exec","lint-staged"\]/)
  assert.doesNotMatch(result.stdout, /CALLED go/)
})
test('Go package checks run sequentially and failures stop the chain', t => {
  const { stage, run } = fixture(t)
  stage('internal/a/a.go', 'package a')
  const good = run()
  assert.equal(good.status, 0, good.stderr)
  assert.match(good.stdout, /CALLED golangci-lint \["run","\.\/internal\/a"\]/)
  assert.match(good.stdout, /CALLED go \["test","-timeout=180s","\.\/internal\/a"\]/)
  const bad = run({ FAIL_COMMAND: 'golangci-lint' })
  assert.equal(bad.status, 1)
  assert.doesNotMatch(bad.stdout, /CALLED go \[/)
})
test('timeout fails rather than silently skipping checks', t => {
  const { stage, run } = fixture(t)
  stage('internal/a/a.go', 'package a')
  const result = run({ BUSY_COMMAND: 'golangci-lint', MEMOH_CHECK_TIMEOUT_SECONDS: '0.1' })
  assert.equal(result.status, 1)
  assert.match(result.stderr, /exceeded/)
  assert.doesNotMatch(result.stdout, /CALLED go \[/)
})

test('full mode runs all three checks even without staged changes', t => {
  const { run } = fixture(t)
  const result = run({ MEMOH_FULL_CHECKS: '1' })
  assert.equal(result.status, 0, result.stderr)
  assert.match(result.stdout, /CALLED pnpm \["exec","eslint","\."\]/)
  assert.match(result.stdout, /CALLED golangci-lint \["run","\.\/\.\.\."\]/)
  assert.match(result.stdout, /CALLED go \["test","-timeout=900s","\.\/\.\.\."\]/)
})
test('deleted resource still selects its package through the index', t => {
  const { cwd, git, stage, run } = fixture(t)
  stage('internal/a/a.go', 'package a')
  stage('internal/a/prompt.md', 'prompt')
  git('-c', 'user.name=Hook test', '-c', 'user.email=hook@example.invalid', '-c', 'core.hooksPath=/dev/null', 'commit', '-qm', 'fixture')
  rmSync(join(cwd, 'internal/a/prompt.md'))
  git('add', '-u')
  const result = run({}, ['--plan'])
  assert.equal(result.status, 0, result.stderr)
  assert.deepEqual(JSON.parse(result.stdout), { go: ['./internal/a'], web: 'skip' })
})
