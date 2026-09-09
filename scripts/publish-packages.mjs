#!/usr/bin/env node
import { execFileSync, spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { detectPublishAuthMode } from './publish-auth.mjs'

const ROOT_DIR = dirname(dirname(fileURLToPath(import.meta.url)))

// packages/ui is a git submodule, but it intentionally stays in this publish
// allowlist. Memoh releases publish the pinned UI package under the Memoh
// release version, so the npm package represents the UI sources selected by
// this host release. If @felinic/ui moves to an independent release cadence,
// remove it here and from .github/workflows/release.yml in the same change.
const CANDIDATE_DIRS = [
  'apps/desktop',
  'apps/web',
  'packages/runtime',
  'packages/sdk',
  'packages/ui',
  'packages/icons',
  'packages/config',
]

const log = {
  info: (msg) => console.log(`\x1b[36m[publish]\x1b[0m ${msg}`),
  skip: (msg) => console.log(`\x1b[33m[skip]\x1b[0m ${msg}`),
  ok: (msg) => console.log(`\x1b[32m[ok]\x1b[0m ${msg}`),
  fail: (msg) => console.log(`\x1b[31m[fail]\x1b[0m ${msg}`),
}

const dryRun = process.argv.includes('--dry-run') || process.env.PUBLISH_PACKAGES_DRY_RUN === '1'
const publishScope = process.env.NPM_PUBLISH_SCOPE?.trim() || null

// Auth model: two modes.
// - Token mode (local/manual): NODE_AUTH_TOKEN is set and used directly.
// - OIDC mode (CI trusted publishing): pnpm exchanges the GitHub
//   Actions OIDC token (ACTIONS_ID_TOKEN_REQUEST_TOKEN, available when the job
//   has id-token: write) for a short-lived npm credential at publish time.
//   Every published package must have felinics/Memoh + release.yml registered
//   as its trusted publisher on npmjs.com, or the exchange is rejected.
// OIDC mode changes two behaviors below: the token preflight is impossible
// (npm whoami needs a token), so it is skipped; and --provenance is added,
// which both requires OIDC and records the build provenance on npm.
const authMode = detectPublishAuthMode()
const oidcMode = authMode === 'oidc'

function prereleaseTag(version) {
  const override = process.env.NPM_PUBLISH_TAG?.trim()
  if (override) {
    return override
  }

  const match = version.match(/^\d+\.\d+\.\d+-([^+]+)(?:\+.+)?$/)
  if (!match) {
    return null
  }

  const [identifier] = match[1].split('.')
  return /^[a-z][a-z0-9._-]*$/i.test(identifier) ? identifier : 'next'
}

function publishOptions(version) {
  const args = ['--access', 'public', '--no-git-checks']
  if (oidcMode) {
    args.push('--provenance')
  }
  const tag = prereleaseTag(version)
  if (tag) {
    args.push('--tag', tag)
  }
  return args
}

function isVersionPublished(name, version) {
  try {
    const out = execFileSync('npm', ['view', `${name}@${version}`, 'version'], {
      stdio: ['ignore', 'pipe', 'ignore'],
      encoding: 'utf8',
    }).trim()
    return out === version
  } catch {
    return false
  }
}

// Scope of a package name, e.g. "@felinic/ui" -> "@felinic". Unscoped -> null.
function scopeOf(name) {
  return typeof name === 'string' && name.startsWith('@') ? name.split('/')[0] : null
}

// Identity behind the current NPM token. Returns null when the token is
// missing/invalid, which is itself a hard preflight failure.
function npmWhoami() {
  try {
    return execFileSync('npm', ['whoami'], {
      stdio: ['ignore', 'pipe', 'ignore'],
      encoding: 'utf8',
    }).trim()
  } catch {
    return null
  }
}

// Best-effort proxy for "this token may publish into <scope>". Listing a
// scope's packages requires an authenticated token that belongs to the
// scope's org/user; a brand-new org with zero packages still returns success
// (empty list), while a nonexistent scope or an outside token fails. We only
// use the exit code. This can't *prove* the publish role, but it reliably
// catches the two failure modes that would otherwise cause a half-publish:
// a dead token, and a token that can't see the scope at all.
function scopeReachable(scope) {
  const r = spawnSync('npm', ['access', 'list', 'packages', scope], {
    cwd: ROOT_DIR,
    stdio: ['ignore', 'ignore', 'ignore'],
  })
  return r.status === 0
}

// Preflight: before publishing ANY package, verify the token can reach every
// scope we are about to publish into. Fail closed — if we can't confirm a
// scope, abort the whole run so npm never ends up with a partial release
// (e.g. @memohai/* published but @felinic/ui rejected at 403). Token-mode
// only: OIDC trusted publishing has no token to check, and dry-run skips it
// so local token-free runs keep working. See the mode note at the top.
function preflightScopes(plan) {
  const scopes = [...new Set(plan.map((p) => scopeOf(p.name)).filter(Boolean))]
  if (scopes.length === 0) {
    return true
  }

  const who = npmWhoami()
  if (!who) {
    log.fail('preflight: no valid npm token (npm whoami failed) — aborting before any publish')
    return false
  }
  log.info(`preflight: authenticated as ${who}`)

  const blocked = []
  for (const scope of scopes) {
    if (scopeReachable(scope)) {
      log.ok(`preflight: ${scope} reachable for ${who}`)
    } else {
      blocked.push(scope)
      log.fail(`preflight: ${scope} NOT reachable for ${who} (scope/org missing or token lacks access)`)
    }
  }

  if (blocked.length > 0) {
    log.fail(`preflight failed for: ${blocked.join(', ')} — aborting before any publish`)
    log.fail('Ensure each scope/org exists on npm and the active token has publish rights.')
    return false
  }

  return true
}

function publish(dir, version) {
  const args = ['publish', dir, ...publishOptions(version)]
  if (dryRun) {
    log.info(`[dry-run] ${dir}: pnpm ${args.join(' ')}`)
    return true
  }

  const result = spawnSync(
    'pnpm',
    args,
    { cwd: ROOT_DIR, stdio: 'inherit' },
  )
  return result.status === 0
}

let published = 0
let skipped = 0
let failed = 0

// First pass: resolve the publish plan (what actually needs publishing) so the
// scope preflight below only gates scopes we're really about to touch, and so
// "already published / private" packages never trip it.
const plan = []
for (const dir of CANDIDATE_DIRS) {
  let pkg
  try {
    pkg = JSON.parse(readFileSync(join(ROOT_DIR, dir, 'package.json'), 'utf8'))
  } catch {
    log.skip(`${dir} (no package.json)`)
    skipped++
    continue
  }

  const { name, version, private: isPrivate } = pkg

  if (isPrivate) {
    log.skip(`${name} (private)`)
    skipped++
    continue
  }

  if (publishScope && scopeOf(name) !== publishScope) {
    log.skip(`${name} (outside ${publishScope})`)
    skipped++
    continue
  }

  if (isVersionPublished(name, version)) {
    log.skip(`${name}@${version} (already on registry)`)
    skipped++
    continue
  }

  plan.push({ dir, name, version })
}

// Preflight gate. On real token-mode runs a scope we can't reach aborts
// everything before the first publish, so npm is never left with a partial
// release. OIDC mode skips it: there is no token to check (whoami/access-list
// would fail spuriously), and a missing trusted-publisher entry fails that
// package's publish loudly instead. Dry-run skips it so token-free local/CI
// dry runs keep working.
if (!dryRun && plan.length > 0) {
  if (oidcMode) {
    log.info('OIDC trusted publishing: skipping token preflight; auth happens per-package at publish time')
  } else if (!preflightScopes(plan)) {
    console.log(`\nSummary: 0 published, ${skipped} skipped, ${plan.length} blocked by preflight`)
    process.exit(1)
  }
}

for (const { dir, name, version } of plan) {
  log.info(`${name}@${version}`)
  if (publish(dir, version)) {
    log.ok(`${name}@${version}`)
    published++
  } else {
    log.fail(`${name}@${version}`)
    failed++
    // Fail-stop: a mid-run publish failure already means a partial release; keep
    // going and we'd only widen the gap. Stop so the summary points at the first
    // break. (Preflight makes token/scope failures fail *before* this point.)
    break
  }
}

console.log(`\nSummary: ${published} published, ${skipped} skipped, ${failed} failed`)
process.exit(failed > 0 ? 1 : 0)
