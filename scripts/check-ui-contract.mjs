#!/usr/bin/env node
// UI contract guard — keeps the cross-cutting design primitives from drifting
// back into per-component free-styling (the class of bug that shipped a
// hand-rolled icon hover and stray radii). Single sources live in:
//   · packages/ui/src/style.css         (interaction contracts, type/radius tokens)
//   · packages/ui/src/components/button  (the one icon button: Button variant=ghost)
//
// THREE rule families with different SCOPE:
//   · CONTRACT rules — packages/ui ONLY (raw color / arbitrary radius / invented
//     shadow / disabled opacity / field-edge / selection ring). The library is the
//     single source the app consumes.
//   · px-SCALING rules — packages/ui, apps/web AND apps/desktop renderer. The root
//     font-size is driven by --memoh-ui-font-size, so rem scales with the user's font
//     setting / browser zoom while a hardcoded px does NOT. A px on a property that
//     gates TEXT therefore stops growing while the surrounding text grows → clipped /
//     misaligned controls.
//   · APP-INJECTION rules — apps/web AND apps/desktop renderer (ratcheted).
//     Interaction chrome, color and elevation each have ONE source in the library;
//     a page that hand-injects hover:/active: fills, raw colors or shadow-* onto a
//     component tag forks that source — the exact drift where one control hovers
//     differently than its neighbour. Existing debt is grandfathered; new injection
//     HARD-fails.
//   · INVALID-COLOR rule — packages/ui AND apps/web (both scopes, no ratchet).
//     hsl(var(--x)) wrapping an oklch token is an invalid color function; the
//     browser drops the whole declaration silently (no error, no fallback),
//     which is how it survives visual QA. No escape hatch — there is no
//     legitimate hsl(var(--x)) post-oklch-migration.
//   · Z-INDEX rules — packages/ui AND apps/web (both scopes, no baseline). The
//     z-index ladder (packages/ui/AGENTS.md "z 梯") replaced 11 ad-hoc raw
//     values with 5 semantic tiers; a bare z-10/20/30/40/50 or z-[N] utility
//     HARD-fails (the ladder started at zero debt and stays there), and a raw
//     `z-index: N` CSS literal (not wrapped in var()) WARNs. Escape hatch:
//     `ui-allow-z` for a genuine context-internal exception (an element only
//     ordered against its own children/siblings, never another component).
//   · ALPHA rules — packages/ui AND apps/web (both scopes, ratcheted). Tailwind's
//     built-in `/NN` opacity suffix on a semantic color name (`text-foreground/92`,
//     `bg-muted/40`) is hand-written alpha the raw-color regex above cannot see
//     (that regex only catches bracket/hex literals) — packages/ui/AGENTS.md
//     "Alpha policy" names this the structural blind spot that let the same
//     visual role re-derive its own percentage at every call site instead of
//     sharing one token. A bare `PREFIX-color/NN` HARD-fails unless allowlisted
//     (`ring-ring/*` is naturally excluded — "ring" isn't in the color list below
//     — and overlay scrims / the neutral `--overlay-*` ladder don't match this
//     shape at all). Existing debt is grandfathered in ui-alpha-baseline.json,
//     same ratchet shape as px/app-injection/spin. Escape hatch: `ui-allow-alpha`.
//
// px rules — HARD FAIL (exit 1):
//   · text-[Npx]      font-size never scales        → use a --text-* token
//   · leading-[Npx]   line-height never scales       → use rem / unitless
//   · h/min-h-[Npx], p*/gap/space-[Npx]  (N >= 5)     → a text box / its spacing won't
//     grow with the font → use rem (the spacing scale, min-h, etc).
// NOT flagged: width / cap props (w / min-w / max-w / max-h-[Npx]) are a reflow &
//   sizing concern, not text-coupled, so they are left to review; and decorative
//   1–4px hairlines / indicator bars, plus border-/ring-/outline-/translate-/inset-/
//   size-/blur-/rounded-* (never a text box). Escape hatch: put `ui-allow-px` in a
//   comment on the SAME line for a sanctioned exception (e.g. a fixed chart height).
//
// BASELINE RATCHET (per-family): scripts/ui-px-baseline.json (text-coupled px),
//   scripts/ui-app-baseline.json (app-scope injection), scripts/ui-spin-baseline.json
//   (hand-spun loaders), and scripts/ui-alpha-baseline.json (hand-written alpha)
//   each record the violation count per file. A file may keep its grandfathered
//   count, but ADDING more (count grows, or a brand-new file) HARD-fails — so new
//   code can't regress while existing debt is burned down per cluster. Regenerate
//   ALL of them after a cleanup pass with:
//     node scripts/check-ui-contract.mjs --write-baseline
//
// CONTRACT rules (packages/ui), unchanged:
//   1. disabled-context opacity that is not 40   → disabled is opacity-40 everywhere
//   2. arbitrary radius rounded-[Npx] / rounded-[calc(...)] → use rounded-sm/md/lg
//   5. raw color in a .vue arbitrary class       → use a palette token
//   6. raw color in the style.css COMPONENT layer → define + use a token
//   7. box-shadow with a raw color               → never invent chrome (use a token)
//   4. likely hand-rolled icon-button hover (WARN)
//   8. raw shadow utility (WARN)  9. border-input on a control (WARN)
//  10. ring-offset-* selection halo (WARN)
//  11. hand-rolled SettingsRow — min-h-[3.75rem] (or its bare-scale twin min-h-15)
//      outside the owner (WARN, apps/web); escape hatch `ui-allow-shape` for a
//      genuinely different row — the marker must carry a written reason (enforced)
//  12. hand-spun loader — a literal `animate-spin` in apps/web (ratcheted). Every
//      sanctioned loading state renders through an owner (`Spinner` atom /
//      `Button :loading` / `InlineLoadingRow` / `PanePlaceholder`), and none of
//      those emit the literal from app code — so a bare `animate-spin` is a
//      hand-rolled loader skipping the four-rung ladder. Existing long-tail debt
//      is grandfathered in scripts/ui-spin-baseline.json; adding more HARD-fails.
//      Deliberate exceptions (bare-glyph rung, deferred chat surfaces) use
//      `ui-allow-spin` on the line.
//  13. hand-written alpha — a semantic-color `/NN` suffix (`text-foreground/92`,
//      `bg-muted/40`) in packages/ui OR apps/web (ratcheted, both scopes — the
//      only rule family that is). packages/ui/AGENTS.md "Alpha policy" is the
//      rationale: transparency should come from a `-soft`/`-border`/`-muted`
//      token, not a hand-picked percentage re-derived at each call site.
//      Allowlisted: `ring-ring/*` (not matched — "ring" isn't in the color
//      list), overlay scrims, and the neutral `--overlay-*` ladder (none of
//      these match the semantic-color shape this rule scans for). Existing
//      debt is grandfathered in scripts/ui-alpha-baseline.json; adding more
//      HARD-fails. Escape hatch: `ui-allow-alpha` on the line.
//
// Run: node scripts/check-ui-contract.mjs   (wired into `mise run lint`)
import { existsSync, readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = join(fileURLToPath(new URL('.', import.meta.url)), '..')
// FULL_DIRS get every contract rule (the library is the single source). APP_DIRS get
// the px-scaling rules AND the app-scope injection rules (interaction / raw color /
// invented shadow) — the orchestration layer must consume the library's chrome, not
// hand-inject it. Both app families are ratcheted (below) so existing debt is
// grandfathered while new injection is blocked.
const FULL_DIRS = ['packages/ui/src']
const APP_DIRS = ['apps/web/src', 'apps/desktop/src/renderer/src']
const EXT = /\.(vue|ts)$/
const PX_BASELINE_PATH = join(ROOT, 'scripts/ui-px-baseline.json')
const APP_BASELINE_PATH = join(ROOT, 'scripts/ui-app-baseline.json')
const SPIN_BASELINE_PATH = join(ROOT, 'scripts/ui-spin-baseline.json')
const ALPHA_BASELINE_PATH = join(ROOT, 'scripts/ui-alpha-baseline.json')
const WRITE_BASELINE = process.argv.includes('--write-baseline')

// Exact arbitrary-radius tokens that are legitimately allowed.
//   rounded-[calc(var(--radius)-5px)] = the tuned 5px in-field small-control radius
//   (InputGroup clear/reveal + NumberField steppers). Smaller than the 8px family
//   radius on purpose so a 24px box does not read as a pill.
const RADIUS_ALLOW = new Set([
  'rounded-[inherit]',
  'rounded-[2px]',
  'rounded-[calc(var(--radius)-5px)]',
])
// The only files allowed to author the canonical settings-row height (3.75rem).
// Anywhere else that literal appears, a row was hand-rolled instead of composing
// <SettingsRow> — the 同形异码 the owner vocabulary exists to kill. See rule 11.
// inline-loading-row/index.vue joined 2026-07-06: its `card-row` surface prop
// renders the identical row height for a loading placeholder, so the literal now
// has a second legitimate owner instead of being hand-copied onto every caller.
const OWNER_ROW_FILES = new Set([
  'apps/web/src/components/settings/row.vue',
  'apps/web/src/components/settings/expandable-row.vue',
  'apps/web/src/components/inline-loading-row/index.vue',
])
// The only app files allowed to render a spinner directly (the four-rung loading
// ladder's owners). Everywhere else `animate-spin` means a loader was hand-spun
// instead of composed — see rule 12. The Spinner ATOM lives in packages/ui (outside
// app scope), so this set is just the app-side owners that wrap it with layout.
const OWNER_SPIN_FILES = new Set([
  'apps/web/src/components/inline-loading-row/index.vue',
  'apps/web/src/components/pane-placeholder/index.vue',
])
// Box props (height / padding / gap / space) below this px size are hairlines or
// indicator bars (a 2px tab underline, a 3px link offset), never a text container —
// so they are decorative and not flagged. 5px+ is where a real text box starts.
const MIN_BOX_PX = 5

const hard = []
const warn = []
// Ratcheted candidates — collected per family so the baseline can grandfather existing
// debt before promoting overflow into `hard`. pxHard: text-coupled px (both scopes).
// appHard: app-scope injection — interaction / raw color / invented shadow (apps/web).
// spinHard: hand-spun loaders — bare `animate-spin` outside the loading owners (apps/web).
// alphaHard: hand-written semantic-color `/NN` alpha (both scopes — see rule 13).
const pxHard = []
const appHard = []
const spinHard = []
const alphaHard = []

function walk(dir, full) {
  for (const name of readdirSync(dir)) {
    const fullPath = join(dir, name)
    const st = statSync(fullPath)
    if (st.isDirectory()) walk(fullPath, full)
    else if (EXT.test(name)) scan(fullPath, full)
  }
}

// Strip comments so descriptive prose ("was the old border-input / shadow-md")
// never trips a smell check. Tracks // line comments and /* */ + <!-- --> blocks
// across lines (block state lives in the caller, reset per file). Code after //
// (incl. URLs) is dropped — harmless, we never assert on URLs.
function makeCodeStripper() {
  let blockEnd = null
  return (line) => {
    let out = ''
    let i = 0
    while (i < line.length) {
      if (blockEnd) {
        const idx = line.indexOf(blockEnd, i)
        if (idx === -1) return out
        i = idx + blockEnd.length
        blockEnd = null
        continue
      }
      if (line.startsWith('//', i)) return out
      if (line.startsWith('/*', i)) { blockEnd = '*/'; i += 2; continue }
      if (line.startsWith('<!--', i)) { blockEnd = '-->'; i += 4; continue }
      out += line[i]
      i++
    }
    return out
  }
}

function scan(file, full) {
  const rel = relative(ROOT, file)
  const src = readFileSync(file, 'utf8')
  const lines = src.split('\n')
  const codeOf = makeCodeStripper()
  // Forward window for the shape escape hatch. Unlike px/style — whose token sits
  // on a line that CAN carry a trailing comment — the shape fingerprint
  // (min-h-[3.75rem]) always lives inside a Vue element's `class="…"` attribute
  // line, which cannot hold an inline comment. So `ui-allow-shape` is written as
  // the element's leading comment (the Vue-natural place for a "why this shape"
  // note) and suppresses the next min-h within a window of lines. The window must
  // clear a MULTI-LINE justification comment PLUS the element's multi-attribute
  // open tag (comment lines → <div → v-for → :key → class), so it is generous (10):
  // the marker line is the comment's FIRST line, and a good "why" note runs a few
  // lines. Over-reach is harmless — two distinct 3.75rem rows abutting within ~10
  // lines is effectively impossible, so a stray second row can't hide behind a mark.
  let shapeAllowUntil = -1
  // Forward window for the z-index ladder escape hatch — declared alongside
  // shapeAllowUntil; see the ui-allow-z comment below for why it needs a window.
  let zAllowUntil = -1
  lines.forEach((rawLine, i) => {
    const line = codeOf(rawLine)
    const ln = i + 1
    // Same-line escape hatch for a sanctioned px (kept on the RAW line so the
    // comment survives the code-stripper).
    const allowPx = rawLine.includes('ui-allow-px')
    const allowStyle = rawLine.includes('ui-allow-style')
    const allowSpin = rawLine.includes('ui-allow-spin')
    const allowAlpha = rawLine.includes('ui-allow-alpha')
    if (rawLine.includes('ui-allow-shape')) {
      shapeAllowUntil = i + 10
      // The exemption only earns its keep with the WHY written next to it — a bare
      // marker is indistinguishable from "silenced the guard to make it shut up",
      // and the recorded reason is what lets the next reader re-judge the shape.
      // Every legitimate marker in the codebase already reads
      // `ui-allow-shape: <reason…>`; enforce that form (≥ 15 chars of reason).
      const after = rawLine
        .slice(rawLine.indexOf('ui-allow-shape') + 'ui-allow-shape'.length)
        .replace(/-->.*$/, '')
        .replace(/\*\/.*$/, '')
        .replace(/^[\s:：—–-]+/, '')
        .trim()
      if (after.length < 15)
        warn.push(`${rel}:${ln}  ui-allow-shape without a written reason — the marker must carry its why on the same line ("ui-allow-shape: <what this shape is and why no owner fits>")`)
    }
    const allowShape = i <= shapeAllowUntil
    // Forward window for the z-index ladder's escape hatch — same trick as
    // ui-allow-shape and for the same reason: z-[1]/z-[2] usually live inside
    // a class="…" attribute (or a long cva() string) that can't carry a
    // trailing comment, so `ui-allow-z` is written as a leading comment and
    // suppresses the ladder check for a window of lines. Wider than
    // ui-allow-shape's (20 vs 10): the tab-local-paint-order exception this
    // exists for (workspace-tab.vue) carries a long multi-paragraph WHY above
    // each element, so the marker-to-class gap runs longer than a typical
    // one-line shape comment.
    if (rawLine.includes('ui-allow-z')) zAllowUntil = i + 20
    const allowZ = i <= zAllowUntil
    for (const tok of line.split(/[\s'"`]+/)) {
      if (!tok) continue

      // ── px-scaling rules (run in BOTH scopes) ────────────────────────────
      if (!allowPx) {
        // font-size / line-height in px never scale — any value is wrong.
        if (/(?:^|:)text-\[\d+(?:\.\d+)?px\]/.test(tok))
          pxHard.push({ rel, ln, msg: `px font-size never scales (use a --text-* token) → ${tok}` })
        if (/(?:^|:)leading-\[\d+(?:\.\d+)?px\]/.test(tok))
          pxHard.push({ rel, ln, msg: `px line-height never scales (use rem/unitless) → ${tok}` })
        // height / padding / gap / space: a text box (>=5px) that won't grow.
        let m
        if ((m = tok.match(/(?:^|:)(?:min-h|h)-\[(\d+(?:\.\d+)?)px\]/)) && Number(m[1]) >= MIN_BOX_PX)
          pxHard.push({ rel, ln, msg: `px height won't grow with the font (use rem / min-h) → ${tok}` })
        if ((m = tok.match(/(?:^|:)p[xytblrse]?-\[(\d+(?:\.\d+)?)px\]/)) && Number(m[1]) >= MIN_BOX_PX)
          pxHard.push({ rel, ln, msg: `px padding won't scale (use the rem spacing scale) → ${tok}` })
        if ((m = tok.match(/(?:^|:)(?:gap(?:-[xy])?|space-[xy])-\[(\d+(?:\.\d+)?)px\]/)) && Number(m[1]) >= MIN_BOX_PX)
          pxHard.push({ rel, ln, msg: `px gap/space won't scale (use the rem spacing scale) → ${tok}` })
      }

      // ── invalid oklch-era leftover (BOTH scopes; HARD, no ratchet) ───────
      //   Tokens moved from hsl components to oklch; wrapping a var(--x)
      //   reference in hsl(...) is an invalid color function that the browser
      //   silently drops the WHOLE declaration for — no console warning, no
      //   visible fallback. Caught two live instances this way (sidebar rail
      //   hover outline, about-page link color) that survived visual QA
      //   because nothing errors. No escape hatch: there is no legitimate
      //   reason to wrap an oklch var() in hsl() post-migration.
      if (/hsl\(var\(--/.test(tok))
        hard.push(`${rel}:${ln}  oklch token wrapped in hsl() is invalid — the browser drops the whole declaration; use var(--x) directly → ${tok}`)

      // ── z-index ladder (BOTH scopes; HARD, no baseline) ──────────────────
      //   packages/ui/AGENTS.md "z 梯": five semantic tiers (--z-raised/-sticky/
      //   -panel/-overlay/-top) replaced 11 ad-hoc raw values. A bare
      //   z-10/20/30/40/50 or bracket-escaped z-[N] utility means a new
      //   floating element picked a number instead of a tier — exactly how the
      //   pre-ladder chaos accumulated. No baseline: the migration that
      //   introduced this rule already folded every legitimate call site onto
      //   a token, so any further hit is NEW debt, not grandfathered history.
      //   Escape hatch: `ui-allow-z` for the rare value that only orders a
      //   component against ITS OWN children, never another component.
      if (!allowZ && (/(?:^|:)z-(?:10|20|30|40|50)$/.test(tok) || /(?:^|:)z-\[[^\]]+\]$/.test(tok)))
        hard.push(`${rel}:${ln}  raw z-index utility off the ladder (use z-(--z-raised / --z-sticky / --z-panel / --z-overlay / --z-top)) → ${tok}`)

      // ── hand-written alpha (BOTH scopes; ratcheted) ──────────────────────
      //   packages/ui/AGENTS.md "Alpha policy": a semantic-color name carrying
      //   Tailwind's built-in `/NN` opacity suffix is hand-picked transparency
      //   the raw-color regex (rule 5/6, bracket/hex only) cannot see. Every
      //   role this shape can express already has, or should get, a
      //   `-soft`/`-border`/`-muted` token (color-mix off the base semantic
      //   color, so it tracks .dark / per-scheme automatically and reproduces
      //   the exact pixel the /NN call site produced). `ring-ring` is naturally
      //   excluded — "ring" is not one of the color names below — so the
      //   pre-existing focus-ring exemption needs no special-case code; overlay
      //   scrims and the neutral --overlay-* ladder don't match this shape
      //   either (they're not a semantic-color/NN pair). Escape hatch:
      //   `ui-allow-alpha` for a call site with no repeated role yet (nothing
      //   to name a token after) or a deliberate one-off.
      if (!allowAlpha &&
          /(?:^|:)(?:bg|text|border|divide|ring|shadow|from|to|via)-(?:muted|accent|border|foreground|background|destructive|warning|primary|success|info|card|popover|sidebar[a-z-]*)(?:-foreground)?\/[0-9]+$/.test(tok))
        alphaHard.push({ rel, ln, msg: `hand-written alpha (use a -soft/-border/-muted token, or add one — see packages/ui/AGENTS.md § Alpha policy) → ${tok}` })

      // ── app-scope injection rules (apps/web + desktop renderer; ratcheted) ─
      //   The library owns interaction chrome (style.css ::before), color (palette
      //   tokens) and elevation (shadow tokens). A page that hand-injects these onto
      //   a component tag forks the single source — the drift where one control hovers
      //   differently than its neighbour. Escape hatch: `ui-allow-style` on the line.
      if (!full && !allowStyle) {
        if (/(?:^|:)(?:hover|active|group-hover):(?:bg|text|border|ring)-/.test(tok))
          appHard.push({ rel, ln, msg: `ad-hoc interaction fill (chrome belongs to the component, not the page) → ${tok}` })
        else if (/(?:^|:)(?:bg|text|border|ring|divide|outline)-(?:white|black)(?:$|\/)/.test(tok) ||
                 /(?:^|:)(?:bg|text|border|ring|divide|outline)-(?:gray|zinc|slate|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose)-\d/.test(tok) ||
                 /-\[(?:#|(?:rgba?|hsla?|oklch|oklab|lab|lch|color-mix)\()/.test(tok))
          appHard.push({ rel, ln, msg: `raw color (use a palette token, not a fixed shade) → ${tok}` })
        else if (/(?:^|:)shadow-(?:2xs|xs|sm|md|lg|xl|2xl)$/.test(tok))
          appHard.push({ rel, ln, msg: `invented shadow (use an elevation token or shadow-none) → ${tok}` })
      }

      // 11. hand-rolled SettingsRow (WARN). The canonical row height 3.75rem is an
      //   arbitrary, distinctive literal — it only appears because a row was copied
      //   out of <SettingsRow> as raw divs instead of composed. Token-level rules
      //   can't see a whole owner shape, but this one geometry has a rare fingerprint
      //   that catches the SettingsRow slice of the 同形异码 debt at ~0% false
      //   positive. WARN, not HARD: a genuinely denser/different row may coincide —
      //   triage against the "stay hand-written" tells in the owner skill, or silence
      //   a confirmed-different one with `ui-allow-shape` on the line. `min-h-15` is
      //   the Tailwind-v4 bare-scale spelling of the same 3.75rem — same fingerprint,
      //   same rule, or the guard is trivially dodged by dropping the brackets.
      if (!full && !allowShape && !OWNER_ROW_FILES.has(rel) && /(?:^|:)min-h-(?:\[3\.75rem\]|15$)/.test(tok))
        warn.push(`${rel}:${ln}  possible hand-rolled SettingsRow (min-h-[3.75rem]/min-h-15 outside the owner) — compose <SettingsRow>, or mark ui-allow-shape if it is a genuinely different row → ${tok}`)

      // 12. hand-spun loader (ratcheted). All four rungs of the loading ladder render
      //   their spinner through an owner (Spinner atom / Button :loading /
      //   InlineLoadingRow / PanePlaceholder) and none of them emit the literal
      //   `animate-spin` from app code — so a bare `animate-spin` in apps/web is a
      //   loader hand-rolled past the ladder. Existing long-tail debt (deferred chat
      //   surfaces, bare-glyph sites) is grandfathered in ui-spin-baseline.json;
      //   NEW hand-spun loaders hard-fail. Sanctioned exception → `ui-allow-spin`.
      if (!full && !allowSpin && !OWNER_SPIN_FILES.has(rel) && /(?:^|:)animate-spin$/.test(tok))
        spinHard.push({ rel, ln, msg: `hand-spun loader (pick a rung: Spinner atom / Button :loading / InlineLoadingRow / PanePlaceholder) → ${tok}` })

      // ── contract rules (packages/ui only) ────────────────────────────────
      if (!full) continue
      // 1. disabled opacity must be 40
      const op = tok.match(/opacity-(\d+)$/)
      if (op && /disabled/.test(tok) && op[1] !== '40')
        hard.push(`${rel}:${ln}  disabled opacity must be 40 → ${tok}`)
      // 2. arbitrary radius
      if (/^(?:[a-z-]+:)*rounded-\[/.test(tok) && !RADIUS_ALLOW.has(tok.replace(/^(?:[a-z-]+:)*/, '')))
        hard.push(`${rel}:${ln}  arbitrary radius (use rounded-sm/md/lg) → ${tok}`)
      // 5. raw color in a Tailwind arbitrary class (bg-[#..], text-[oklch(..)], …)
      if (/-\[(?:#|(?:rgba?|hsla?|oklch|oklab|lab|lch|color-mix)\()/.test(tok))
        hard.push(`${rel}:${ln}  raw color in arbitrary class (use a palette token) → ${tok}`)
      // 8. raw tailwind shadow scale utility — elevation is tokenized
      //    (shadow-[var(--shadow-*)] and shadow-none are the allowed forms).
      if (/(?:^|:)shadow-(?:2xs|xs|sm|md|lg|xl|2xl)$/.test(tok))
        warn.push(`${rel}:${ln}  raw shadow utility (use an elevation token or shadow-none) → ${tok}`)
      // 9. structural border on a control body — controls use the field-edge family
      if (/(?:^|:)border-input$/.test(tok))
        warn.push(`${rel}:${ln}  border-input on a control (use the field-edge contract) → ${tok}`)
      // 10. selection/active via an offset ring halo — use an indicator / --ui-selected
      if (/(?:^|:)ring-offset(?:-|$)/.test(tok))
        warn.push(`${rel}:${ln}  ring-offset (selection via offset halo — use an indicator) → ${tok}`)
    }
    // 4. likely hand-rolled icon-button hover (icon present + ad-hoc hover fill)
    if (full && /\[&[_>]svg/.test(line) && /hover:bg-(accent|\[)/.test(line) && !/data-slot="button"/.test(line))
      warn.push(`${rel}:${ln}  possible hand-rolled icon hover — reuse <Button variant="ghost">`)
  })
}

// style.css is where tokens are DEFINED (raw values legal in :root/.dark/@theme)
// and where component styling is AUTHORED (raw values illegal there). Scan it with
// block awareness so we only flag raw color / invented box-shadow in the
// component layer, never in the token-definition blocks.
const COLOR_FN = /(?:^|[^\w-])(?:rgba?|hsla?|oklch|oklab|lab|lch|color-mix)\s*\(/
const HEX = /#[0-9a-fA-F]{3,8}\b/
function scanCss(file) {
  const rel = relative(ROOT, file)
  const lines = readFileSync(file, 'utf8').split('\n')
  let depth = 0
  let tokenBlockDepth = -1 // depth at which the active token-definition block opened
  lines.forEach((line, i) => {
    const ln = i + 1
    const trimmed = line.trim()
    const isComment = trimmed.startsWith('/*') || trimmed.startsWith('*') || trimmed.startsWith('//')
    const inTokenBlock = tokenBlockDepth !== -1
    const opensTokenBlock = /^(?::root|\.dark|@theme)\b/.test(trimmed) && line.includes('{')
    if (!inTokenBlock && !isComment) {
      // 6. raw color literal in the component layer → must be a token
      if (HEX.test(line) || COLOR_FN.test(line))
        hard.push(`${rel}:${ln}  raw color in component CSS (define a token) → ${trimmed.slice(0, 64)}`)
      // 7. box-shadow with a RAW color = invented chrome. A shadow built purely
      //    from var() tokens (e.g. the field edge: var(--field-edge)) is fine.
      const bs = line.match(/box-shadow:\s*([^;]+);?/)
      if (bs && (HEX.test(bs[1]) || COLOR_FN.test(bs[1])))
        hard.push(`${rel}:${ln}  box-shadow with raw color (use a token) → ${bs[1].trim().slice(0, 50)}`)
    }
    if (opensTokenBlock && tokenBlockDepth === -1) tokenBlockDepth = depth
    for (const ch of line) {
      if (ch === '{') depth++
      else if (ch === '}') {
        depth--
        if (tokenBlockDepth !== -1 && depth <= tokenBlockDepth) tokenBlockDepth = -1
      }
    }
  })
}

// z-index ladder — raw CSS literal (WARN, both scopes). A standalone pass, not
// folded into scanCss above: scanCss's color/shadow HARD rules are packages/ui-
// only (the library is their single source) and would misfire on apps/web CSS
// files' unrelated, never-audited color literals (e.g. dockview-theme.css's
// --dock-drop-scrim rgba()) if reused wholesale. This pass checks ONLY for a
// bare `z-index: N` outside var() — WARN, not HARD, because raw CSS can't carry
// the same same-line marker trick as a Tailwind class (`z-index: 0; /* ui-allow-z */`
// IS valid CSS, but our own authored exceptions read better as a leading
// block comment) — so it uses the identical forward-window escape as the
// z-index Tailwind-utility rule in scan() above (widened to 12: the longest
// authored exception, the toaster's uncapped 9999 in style.css, carries a
// multi-line WHY that runs longer than the other z-index comments).
// Deliberately excludes negative literals (`z-index: -1`) — that idiom pins a
// pseudo-element BEHIND its own parent's paint (button ::before chrome), never
// a "which global tier" question, so it was never part of this ladder's scope
// (see Plan 003's own enumeration regex, which used [0-9]+ with no leading -).
const Z_INDEX_LITERAL = /z-index:\s*([0-9]+)/
function scanZIndexCss(file) {
  const rel = relative(ROOT, file)
  const lines = readFileSync(file, 'utf8').split('\n')
  let zAllowUntil = -1
  lines.forEach((line, i) => {
    const ln = i + 1
    if (line.includes('ui-allow-z')) zAllowUntil = i + 12
    const allowZ = i <= zAllowUntil
    const m = line.match(Z_INDEX_LITERAL)
    if (m && !allowZ)
      warn.push(`${rel}:${ln}  raw z-index literal (use var(--z-raised / --z-sticky / --z-panel / --z-overlay / --z-top), or mark ui-allow-z for a local paint-order exception) → z-index: ${m[1]}`)
  })
}

for (const d of FULL_DIRS) walk(join(ROOT, d), true)
for (const d of APP_DIRS) walk(join(ROOT, d), false)
scanCss(join(ROOT, 'packages/ui/src/style.css'))
scanZIndexCss(join(ROOT, 'packages/ui/src/style.css'))
scanZIndexCss(join(ROOT, 'apps/web/src/styles/dockview-theme.css'))

// ── baseline ratchets ────────────────────────────────────────────────────────
// Per family: count violations per file, then either (re)write the baseline or
// promote any file that EXCEEDS its grandfathered count into `hard` (no new debt).
function countByFile(items) {
  const m = {}
  for (const v of items) m[v.rel] = (m[v.rel] || 0) + 1
  return m
}
function writeBaseline(path, byFile, label) {
  const sorted = Object.fromEntries(Object.entries(byFile).sort(([a], [b]) => a.localeCompare(b)))
  writeFileSync(path, `${JSON.stringify(sorted, null, 2)}\n`)
  const total = Object.values(sorted).reduce((a, b) => a + b, 0)
  console.log(`✓ wrote ${label} baseline: ${total} grandfathered violation(s) across ${Object.keys(sorted).length} file(s)`)
}
function ratchet(byFile, path, items, label, allowTag) {
  const baseline = existsSync(path) ? JSON.parse(readFileSync(path, 'utf8')) : {}
  let grandfathered = 0
  for (const [rel, count] of Object.entries(byFile)) {
    const allowed = baseline[rel] || 0
    if (count > allowed) {
      // The whole file's lines are surfaced; the count guard is what fails (we can't
      // pin WHICH instance is new from a per-file count, but "no new debt" holds).
      for (const v of items) if (v.rel === rel) hard.push(`${v.rel}:${v.ln}  ${v.msg}`)
      hard.push(`${rel}  ✗ ${label} count ${count} exceeds baseline ${allowed} — no NEW ${label} (fix it, or mark the line with ${allowTag})`)
    }
    else grandfathered += count
  }
  return grandfathered
}

const pxByFile = countByFile(pxHard)
const appByFile = countByFile(appHard)
const spinByFile = countByFile(spinHard)
const alphaByFile = countByFile(alphaHard)

if (WRITE_BASELINE) {
  writeBaseline(PX_BASELINE_PATH, pxByFile, 'px')
  writeBaseline(APP_BASELINE_PATH, appByFile, 'app-injection')
  writeBaseline(SPIN_BASELINE_PATH, spinByFile, 'hand-spun-loader')
  writeBaseline(ALPHA_BASELINE_PATH, alphaByFile, 'alpha')
  process.exit(0)
}

const pxGrand = ratchet(pxByFile, PX_BASELINE_PATH, pxHard, 'px', 'ui-allow-px')
const appGrand = ratchet(appByFile, APP_BASELINE_PATH, appHard, 'app-injection', 'ui-allow-style')
const spinGrand = ratchet(spinByFile, SPIN_BASELINE_PATH, spinHard, 'hand-spun-loader', 'ui-allow-spin')
const alphaGrand = ratchet(alphaByFile, ALPHA_BASELINE_PATH, alphaHard, 'alpha', 'ui-allow-alpha')

if (warn.length) {
  console.warn(`\n⚠ UI contract — ${warn.length} warning(s):`)
  for (const w of warn) console.warn(`  ${w}`)
}
if (pxGrand)
  console.log(`\nℹ px baseline: ${pxGrand} grandfathered px violation(s) remaining — burn down per cluster, then re-run with --write-baseline`)
if (appGrand)
  console.log(`ℹ app-injection baseline: ${appGrand} grandfathered injection(s) remaining — burn down per cluster, then re-run with --write-baseline`)
if (spinGrand)
  console.log(`ℹ hand-spun-loader baseline: ${spinGrand} grandfathered loader(s) remaining — adopt a loading-ladder rung per cluster, then re-run with --write-baseline`)
if (alphaGrand)
  console.log(`ℹ alpha baseline: ${alphaGrand} grandfathered hand-written alpha value(s) remaining — burn down per cluster, then re-run with --write-baseline`)
if (hard.length) {
  console.error(`\n✗ UI contract — ${hard.length} violation(s):`)
  for (const h of hard) console.error(`  ${h}`)
  console.error('\nSee packages/ui/src/style.css for the single sources.\n')
  process.exit(1)
}
console.log(`✓ UI contract OK${warn.length ? ` (${warn.length} warning(s))` : ''}`)
