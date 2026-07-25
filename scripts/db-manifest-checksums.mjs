#!/usr/bin/env node
// Recompute the sha256 checksums in db/postgres/manifest.yaml from the
// migration files it publishes.
//
//   node scripts/db-manifest-checksums.mjs           # rewrite stale checksums
//   node scripts/db-manifest-checksums.mjs --check   # report drift, exit 1
//
// The manifest is the contract the Epoch v2 migrator validates before it
// touches a database, so a checksum must never be hand-edited to silence a
// failure: regenerate it only when the migration content changed on purpose.

import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const manifestPath = join(repoRoot, 'db/postgres/manifest.yaml')
const migrationsRoot = join(repoRoot, 'db/postgres')

const checkOnly = process.argv.includes('--check')

const original = readFileSync(manifestPath, 'utf8')
const lines = original.split('\n')

// Entries are a `- path:` line followed by `version:` and `checksum:` lines.
// Walking line-wise keeps comments, ordering, and formatting byte-identical;
// a YAML round-trip would reflow the whole file.
const drift = []
let currentPath = null

for (let i = 0; i < lines.length; i++) {
  const pathMatch = /^(\s*)- path:\s*(\S+)\s*$/.exec(lines[i])
  if (pathMatch) {
    currentPath = pathMatch[2]
    continue
  }
  const checksumMatch = /^(\s*)checksum:\s*(\S+)\s*$/.exec(lines[i])
  if (!checksumMatch || !currentPath) continue

  const [, indent, recorded] = checksumMatch
  const file = join(migrationsRoot, currentPath)
  let content
  try {
    content = readFileSync(file)
  } catch {
    console.error(`missing migration file: ${currentPath}`)
    process.exitCode = 1
    currentPath = null
    continue
  }
  const actual = `sha256:${createHash('sha256').update(content).digest('hex')}`
  if (actual !== recorded) {
    drift.push({ path: currentPath, recorded, actual })
    lines[i] = `${indent}checksum: ${actual}`
  }
  currentPath = null
}

if (drift.length === 0) {
  console.log('manifest checksums are up to date')
  process.exit(process.exitCode ?? 0)
}

for (const { path, recorded, actual } of drift) {
  console.log(`${checkOnly ? 'STALE' : 'updated'} ${path}\n  was ${recorded}\n  now ${actual}`)
}

if (checkOnly) {
  console.error(`\n${drift.length} stale checksum(s); run: mise run db-manifest-checksums`)
  process.exit(1)
}

writeFileSync(manifestPath, lines.join('\n'))
console.log(`\nrewrote ${drift.length} checksum(s) in db/postgres/manifest.yaml`)
