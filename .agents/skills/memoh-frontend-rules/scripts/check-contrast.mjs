#!/usr/bin/env node
// OKLCH -> OKLab -> linear sRGB -> gamma sRGB alpha compositing -> WCAG 2.x contrast ratio.
//
// Usage:
//   1. Audit Memoh Light & Dark token contrast table + shadcn/ui reference:
//      node scripts/check-contrast.mjs
//   2. Compute contrast between any two OKLCH colors (supports foreground alpha):
//      node scripts/check-contrast.mjs "oklch(0.66 0.004 95)" "oklch(0.981 0.0026 63)"
//      node scripts/check-contrast.mjs "oklch(0 0 0 / 0.16)" "oklch(1 0 0)"
//   3. Find the OKLCH lightness L needed to reach a target contrast ratio:
//      node scripts/check-contrast.mjs --solve "oklch(0.66 0.004 95)" "oklch(0.981 0.0026 63)" 4.5

export function oklchToLinearSrgb([L, C, h = 0]) {
  const rad = (h * Math.PI) / 180
  const a = C * Math.cos(rad)
  const b = C * Math.sin(rad)
  const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3
  const m = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3
  const s = (L - 0.0894841775 * a - 1.2914855480 * b) ** 3
  return [
    4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.7076147010 * s,
  ].map(v => Math.min(1, Math.max(0, v)))
}

const encodeSrgb = v => (v <= 0.0031308 ? 12.92 * v : 1.055 * v ** (1 / 2.4) - 0.055)
const decodeSrgb = v => (v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4)

// Composite translucent foreground over opaque background in gamma-encoded sRGB (CSS standard).
export function compositeOver(fgLin, alpha, bgLin) {
  if (alpha >= 1) return fgLin
  return fgLin.map((v, i) => decodeSrgb(alpha * encodeSrgb(v) + (1 - alpha) * encodeSrgb(bgLin[i])))
}

export function relativeLuminance([r, g, b]) {
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

export function contrastRatio(c1Lin, c2Lin) {
  const [hi, lo] = [relativeLuminance(c1Lin), relativeLuminance(c2Lin)].sort((a, b) => b - a)
  return (hi + 0.05) / (lo + 0.05)
}

export function parseOklch(str) {
  const m = str.trim().match(/^oklch\(\s*([0-9.]+)\s+([0-9.]+)\s+([0-9.]+)(?:\s*\/\s*([0-9.]+)(%)?)?\s*\)$/i)
  if (!m) throw new Error(`Invalid oklch() string: ${str}`)
  const L = Number(m[1])
  const C = Number(m[2])
  const h = Number(m[3])
  const alpha = m[4] == null ? 1 : m[5] === '%' ? Number(m[4]) / 100 : Number(m[4])
  return { L, C, h, alpha }
}

export function solveLightness(fgOklch, bgOklch, targetRatio, direction = 'darker') {
  const fg = parseOklch(fgOklch)
  const bg = parseOklch(bgOklch)
  const bgLin = oklchToLinearSrgb([bg.L, bg.C, bg.h])
  let lo = direction === 'darker' ? 0 : fg.L
  let hi = direction === 'darker' ? fg.L : 1
  for (let i = 0; i < 40; i++) {
    const mid = (lo + hi) / 2
    const candLin = compositeOver(oklchToLinearSrgb([mid, fg.C, fg.h]), fg.alpha, bgLin)
    const r = contrastRatio(candLin, bgLin)
    if (direction === 'darker') {
      if (r >= targetRatio) lo = mid
      else hi = mid
    } else {
      if (r >= targetRatio) hi = mid
      else lo = mid
    }
  }
  return Number(((lo + hi) / 2).toFixed(4))
}

const args = process.argv.slice(2)
if (args[0] === '--solve' && args.length >= 4) {
  const [, fgStr, bgStr, targetStr] = args
  const target = Number(targetStr)
  const bg = parseOklch(bgStr)
  const dir = bg.L > 0.5 ? 'darker' : 'lighter'
  const solvedL = solveLightness(fgStr, bgStr, target, dir)
  const fg = parseOklch(fgStr)
  console.log(`Target >= ${target}:1 on ${bgStr} -> oklch(${solvedL} ${fg.C} ${fg.h})`)
  process.exit(0)
}

if (args.length === 2) {
  const fg = parseOklch(args[0])
  const bg = parseOklch(args[1])
  const bgLin = oklchToLinearSrgb([bg.L, bg.C, bg.h])
  const fgLin = compositeOver(oklchToLinearSrgb([fg.L, fg.C, fg.h]), fg.alpha, bgLin)
  const r = contrastRatio(fgLin, bgLin)
  console.log(`Contrast: ${r.toFixed(2)}:1 | >=3:1 (UI/focus/large): ${r >= 3 ? 'PASS' : 'FAIL'} | >=4.5:1 (text): ${r >= 4.5 ? 'PASS' : 'FAIL'}`)
  process.exit(0)
}

// Default: full Memoh Light vs Dark vs shadcn/ui reference audit
const evalPair = (fgStr, bgStr, overrideAlpha) => {
  const fg = parseOklch(fgStr)
  const bg = parseOklch(bgStr)
  const bgLin = oklchToLinearSrgb([bg.L, bg.C, bg.h])
  const alpha = overrideAlpha ?? fg.alpha
  const fgLin = compositeOver(oklchToLinearSrgb([fg.L, fg.C, fg.h]), alpha, bgLin)
  return contrastRatio(fgLin, bgLin).toFixed(2)
}

const M_LIGHT = { bg: 'oklch(0.981 0.0026 63)', card: 'oklch(1 0 0)' }
const M_DARK = { bg: 'oklch(0.12212 0 0)', card: 'oklch(0.21 0 0)' }

const rows = [
  ['Body text (--foreground)',
    `${evalPair('oklch(0.90 0 0)', M_DARK.bg)} / ${evalPair('oklch(0.90 0 0)', M_DARK.card)}`,
    `${evalPair('oklch(0.21 0.004 95)', M_LIGHT.bg)} / ${evalPair('oklch(0.21 0.004 95)', M_LIGHT.card)}`,
    '18.96 / 19.79', 'PASS (>= 4.5:1)'],
  ['Secondary text (--muted-foreground)',
    `${evalPair('oklch(0.70 0 0)', M_DARK.bg)} / ${evalPair('oklch(0.70 0 0)', M_DARK.card)}`,
    `${evalPair('oklch(0.52 0.006 95)', M_LIGHT.bg)} / ${evalPair('oklch(0.52 0.006 95)', M_LIGHT.card)}`,
    '7.63 / 4.73', 'PASS (>= 4.5:1)'],
  ['Input placeholder (--field-placeholder)',
    `${evalPair('oklch(0.66 0.004 95)', M_DARK.bg)} / ${evalPair('oklch(0.66 0.004 95)', M_DARK.card)}`,
    `${evalPair('oklch(0.66 0.004 95)', M_LIGHT.bg)} / ${evalPair('oklch(0.66 0.004 95)', M_LIGHT.card)}`,
    '6.91 / 4.73', 'Dark PASS; Light ~3.0:1 (tune L <= 0.56 for 4.5:1)'],
  ['Input resting edge (--border-hairline)',
    `${evalPair('oklch(0.34543 0 0)', M_DARK.bg)} / ${evalPair('oklch(0.34543 0 0)', M_DARK.card)}`,
    `${evalPair('oklch(0 0 0 / 0.16)', M_LIGHT.bg)} / ${evalPair('oklch(0 0 0 / 0.16)', M_LIGHT.card)}`,
    '1.47 / 1.26', 'Dark solid gray healthier than Light'],
  ['Other field rest edge (--field-edge-rest)',
    `${evalPair('oklch(0.318 0 0)', M_DARK.bg)} / ${evalPair('oklch(0.318 0 0)', M_DARK.card)}`,
    `${evalPair('oklch(0 0 0 / 0.11)', M_LIGHT.bg)} / ${evalPair('oklch(0 0 0 / 0.11)', M_LIGHT.card)}`,
    '1.47 / 1.26', 'Light 11% black (1.28:1) too faint on transparent bg'],
  ['Focused field edge (--field-edge-solid)',
    `${evalPair('oklch(0.60 0 0)', M_DARK.bg)} / ${evalPair('oklch(0.60 0 0)', M_DARK.card)}`,
    `${evalPair('oklch(0 0 0 / 0.78)', M_LIGHT.bg)} / ${evalPair('oklch(0 0 0 / 0.78)', M_LIGHT.card)}`,
    '—', 'PASS (>= 3:1)'],
  ['Button focus ring (ring-ring/30)',
    `${evalPair('oklch(0.86 0.005 285)', M_DARK.bg, 0.3)} / ${evalPair('oklch(0.86 0.005 285)', M_DARK.card, 0.3)}`,
    `${evalPair('oklch(0.575 0 0)', M_LIGHT.bg, 0.3)} / ${evalPair('oklch(0.575 0 0)', M_LIGHT.card, 0.3)}`,
    '1.87 / 1.54', '30% alpha lowers ring contrast (50% alpha -> 3.82:1 in Dark)'],
]

console.log('Memoh Token Contrast Audit (bg / card):')
console.log('-'.repeat(115))
console.log('Token / Role'.padEnd(40), 'Dark (bg/card)'.padEnd(16), 'Light (bg/card)'.padEnd(16), 'shadcn (D/L)'.padEnd(14), 'Notes')
console.log('-'.repeat(115))
for (const [role, d, l, s, note] of rows) {
  console.log(role.padEnd(40), d.padEnd(16), l.padEnd(16), s.padEnd(14), note)
}
