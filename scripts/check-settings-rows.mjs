#!/usr/bin/env node
// Settings row guard — keeps apps/web on the settings layout contract from
// packages/ui (see "Settings list rhythm" in packages/ui/src/style.css):
//
//   · the card shell (SettingsSection) owns the horizontal inset
//     (--settings-inset, 16px);
//   · the list (SettingsSection, or SettingsList outside a card) owns the row
//     rhythm: min height, vertical padding, hairline between rows;
//   · a row only lays out its content.
//
// Two mistakes break that contract silently. Type checks, eslint and
// check-ui-contract cannot see either, because both are about WHERE a node sits
// in the rendered tree, not about the classes on one line:
//
//   ROW OUTSIDE A LIST — a settings row rendered with no list ancestor gets no
//     height, padding or hairline, so neighbouring rows collapse into each other
//     (e.g. rows placed straight into a DialogBody instead of a SettingsList).
//   HAND INSET IN A CARD — a block inside a card that still pads or offsets
//     itself by the card's own inset (px-4 / mx-4 / -mx-4 / --settings-inset)
//     is indented twice, or pulled past the card edge.
//
// METHOD. Every .vue template under apps/web/src is parsed with the
// vue/compiler-sfc that apps/web already depends on, and local .vue imports are
// followed so a component's own template is judged where it is used. Per
// component the guard derives, to a fixed point across imports:
//   · needsList  — rows sit at its root, so every usage must be inside a list;
//   · slots      — per slot name, whether that slot renders inside a list,
//                  outside one, or inherits the usage site;
//   · rootInsets — card-inset classes at its root, including those of
//                  components it renders at its root; an error only when the
//                  component is used inside a card.
// Overlay components (Dialog, Sheet, Popover, …) teleport their content to
// <body>, so their content counts as outside any list. Inside a row and inside
// SettingsBleed, insets are the content's own layout and are not checked.
//
// ESCAPE HATCH. A template comment directly before the element:
//   <!-- ui-allow-loose-row: <why this row needs no list rhythm> -->
//   <!-- ui-allow-card-inset: <why this block offsets by the card inset> -->
// The reason is required. Use it for a deliberate layout (a single row alone
// in a dialog body), not to silence a real collapse. Before a component usage,
// ui-allow-card-inset also covers the insets at that component's root.
//
// Usage: node scripts/check-settings-rows.mjs [src-dir]   (default apps/web/src)
import { readdirSync, readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const SRC = resolve(process.argv[2] ?? join(ROOT, 'apps/web/src'))
const { parse } = createRequire(join(ROOT, 'apps/web/package.json'))('vue/compiler-sfc')

const NODE_ELEMENT = 1
const NODE_COMMENT = 3
const DIRECTIVE = 7
const ATTRIBUTE = 6

const LIST_TAGS = new Set(['SettingsSection', 'SettingsList'])
const PORTAL = /^(Dialog|AlertDialog|Sheet|Popover|DropdownMenu|ContextMenu|Tooltip|HoverCard|Drawer|Command|ConfirmDeleteDialog|ConfirmPopover|FormDialogShell|Teleport)/
// Classes that offset a block by the card's own inset. Smaller values (an icon
// gap, an input's room for its adornment) are content layout, not the inset.
const CARD_INSET = /(?:^|[\s'"`:{,])(-?(?:mx|ml|mr|ms|me|px|pl|pr|ps|pe|p)-(?:4|\(--settings-inset\)))(?=$|[\s'"`},])/g
// A block with its own surface (a full border, a radius, a fill) pads itself
// for its own content; that padding is not the card's inset. A one-sided
// hairline (border-b / border-t, with its border-border colour) is a row-like
// divider, not a surface, so it does not exempt the block.
const OWN_SURFACE = /(?:^|[\s'"`])(?:border|rounded(?:-[\w[\]().-]+)?|bg-(?!transparent)[\w/[\]().-]+)(?=$|[\s'"`])/

const pascal = tag => tag.replace(/-([a-z])/g, (_, c) => c.toUpperCase()).replace(/^[a-z]/, c => c.toUpperCase())

const files = []
const walk = dir => {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, entry.name)
    if (entry.isDirectory()) walk(p)
    else if (p.endsWith('.vue')) files.push(p)
  }
}
walk(SRC)

const comps = new Map()
for (const file of files) {
  let descriptor
  try { descriptor = parse(readFileSync(file, 'utf8'), { filename: file }).descriptor }
  catch { continue }
  if (!descriptor.template?.ast) continue
  const imports = {}
  const script = (descriptor.scriptSetup?.content ?? '') + (descriptor.script?.content ?? '')
  for (const m of script.matchAll(/import\s+(\w+)\s*(?:,\s*\{[^}]*\})?\s+from\s+['"]([^'"]+\.vue)['"]/g)) {
    imports[m[1]] = m[2].startsWith('@/') ? join(SRC, m[2].slice(2)) : resolve(dirname(file), m[2])
  }
  comps.set(file, { ast: descriptor.template.ast, imports, needsList: false, slots: {}, rootInsets: [] })
}

const rel = f => (relative(ROOT, f).startsWith('..') ? relative(SRC, f) : relative(ROOT, f))
const attr = (node, name) => node.props?.find(p => p.type === ATTRIBUTE && p.name === name)?.value?.content
const bound = (node, name) => node.props?.find(p => p.type === DIRECTIVE && p.name === 'bind' && p.arg?.content === name)?.exp?.content
const classes = node => `${attr(node, 'class') ?? ''} ${bound(node, 'class') ?? ''}`
const isBaseRow = node => {
  const tag = pascal(node.tag)
  if (tag === 'SettingsRow' || tag === 'ExpandableSettingsRow' || tag === 'ModelListRow') return true
  return tag === 'InlineLoadingRow' && attr(node, 'surface') === 'card-row'
}

// An allow marker is a comment that is the element's previous non-blank sibling.
function allowed(siblings, index, marker) {
  for (let i = index - 1; i >= 0; i--) {
    const s = siblings[i]
    if (s.type === 2 && !s.content.trim()) continue
    if (s.type !== NODE_COMMENT) return false
    const at = s.content.indexOf(`${marker}:`)
    return at >= 0 && s.content.slice(at + marker.length + 1).trim().length >= 10
  }
  return false
}

// The slot a child of a component usage fills: `<template #name>` / `v-slot:name`
// names one; anything else is the default slot. A dynamic name is unknown (null).
const slotOf = node => {
  if (node.tag !== 'template') return 'default'
  const dir = node.props?.find(p => p.type === DIRECTIVE && p.name === 'slot')
  if (!dir) return 'default'
  if (!dir.arg) return 'default'
  return dir.arg.isStatic === false ? null : dir.arg.content
}

// list: 'yes' | 'no' | 'root' (decided by each usage site of this component)
function analyze(file) {
  const comp = comps.get(file)
  const out = { needsList: false, slots: {}, rootInsets: [], errors: [] }
  const where = node => `${rel(file)}:${node.loc.start.line}`

  const visit = (node, ctx, siblings, index) => {
    if (node.type !== NODE_ELEMENT) return
    if (node.tag === 'slot') {
      // A slot whose name is bound at runtime cannot be matched to a usage.
      if (!bound(node, 'name')) {
        const name = attr(node, 'name') ?? 'default'
        // The same slot rendered twice keeps the first outlet's context.
        out.slots[name] ??= ctx.list === 'root' ? 'inherit' : ctx.list
      }
      return
    }
    const tag = pascal(node.tag)
    const here = PORTAL.test(tag) ? { list: 'no', row: false, bleed: false } : ctx
    const child = comps.get(comp.imports[node.tag] ?? comp.imports[tag])

    const rowish = isBaseRow(node) || child?.needsList
    if (rowish && !here.row && !allowed(siblings, index, 'ui-allow-loose-row')) {
      if (here.list === 'root') out.needsList = true
      else if (here.list === 'no') out.errors.push(`${where(node)}  <${node.tag}> is a settings row outside any SettingsSection / SettingsList — it gets no row height, padding or hairline. Wrap the rows in SettingsList (inside a dialog body) or a SettingsSection.`)
    }

    const insetAllowed = allowed(siblings, index, 'ui-allow-card-inset')
    const insetChecked = !here.row && !here.bleed && !rowish && !LIST_TAGS.has(tag) && !PORTAL.test(tag)
    if (insetChecked && !insetAllowed) {
      const cls = classes(node)
      const hits = [...cls.matchAll(CARD_INSET)].map(m => m[1])
      if (hits.length && !OWN_SURFACE.test(cls)) {
        const msg = `<${node.tag}> ${hits.join(' ')}`
        if (here.list === 'yes') out.errors.push(`${where(node)}  ${msg} inside a settings card — the card already insets its content by --settings-inset. Drop the horizontal inset (keep vertical padding), or wrap edge-to-edge content in SettingsBleed.`)
        else if (here.list === 'root') out.rootInsets.push(`${where(node)}  ${msg}`)
      }
    }
    // The insets at a child component's root land where this usage sits: inside
    // a card they are an error; at this component's own root they become part
    // of its rootInsets, so a wrapper cannot hide them from the card above it.
    if (child && !here.row && !here.bleed && !insetAllowed) {
      if (here.list === 'yes') {
        for (const inset of child.rootInsets) out.errors.push(`${inset} inside a settings card (rendered by ${where(node)}) — the card already insets its content by --settings-inset.`)
      }
      else if (here.list === 'root') {
        for (const inset of child.rootInsets) out.rootInsets.push(`${inset} (via ${where(node)})`)
      }
    }

    let next = here
    if (isBaseRow(node)) next = { ...here, row: true }
    else if (tag === 'SettingsBleed') next = { ...here, bleed: true }
    else if (LIST_TAGS.has(tag)) next = { list: 'yes', row: false, bleed: false }
    else if (node.tag === 'component') {
      const is = node.props?.find(p => p.type === DIRECTIVE && p.arg?.content === 'is')?.exp?.content ?? ''
      if (/\bSettings(?:Section|List)\b/.test(is)) {
        if (/['"][a-z][\w-]*['"]/.test(is) && !allowed(siblings, index, 'ui-allow-loose-row')) {
          out.errors.push(`${where(node)}  <component :is="${is}"> renders a plain element on one branch, so the rows inside lose their list rhythm there. Use SettingsList for the bare branch.`)
        }
        next = { list: 'yes', row: false, bleed: false }
      }
    }
    else if (child) {
      // Each child fills one of the component's slots; it renders in that
      // slot's context, or in the usage site's when the slot inherits it.
      node.children.forEach((c, i) => {
        const name = slotOf(c)
        const slot = name === null ? 'inherit' : (child.slots[name] ?? 'inherit')
        visit(c, slot === 'inherit' ? here : { list: slot, row: false, bleed: false }, node.children, i)
      })
      return
    }

    node.children.forEach((c, i) => visit(c, next, node.children, i))
  }

  const top = comp.ast.children
  top.forEach((c, i) => visit(c, { list: 'root', row: false, bleed: false }, top, i))
  return out
}

// Propagate needsList / slots / rootInsets through imports to a fixed point.
for (let pass = 0; pass < 50; pass++) {
  let changed = false
  for (const [file, comp] of comps) {
    const r = analyze(file)
    if (r.needsList !== comp.needsList || JSON.stringify(r.slots) !== JSON.stringify(comp.slots) || r.rootInsets.join() !== comp.rootInsets.join()) changed = true
    Object.assign(comp, { needsList: r.needsList, slots: r.slots, rootInsets: r.rootInsets })
  }
  if (!changed) break
}

const errors = new Set()
for (const file of comps.keys()) analyze(file).errors.forEach(e => errors.add(e))

// A component whose root holds rows but that no template imports is rendered by
// the router (a page) or dynamically; nothing above it provides a list.
const imported = new Set([...comps.values()].flatMap(c => Object.values(c.imports)))
for (const [file, comp] of comps) {
  if (comp.needsList && !imported.has(file)) errors.add(`${rel(file)}  settings rows at the root of a component no template renders (a routed page?) — nothing above it provides a list. Put the rows in a SettingsSection.`)
}

if (errors.size) {
  console.error(`✗ settings rows: ${errors.size} problem(s)\n`)
  for (const e of [...errors].sort()) console.error(`  ${e}`)
  console.error('\nContract: packages/ui/skills/ui-owners/SKILL.md (SettingsSection / SettingsList / SettingsBleed).')
  process.exit(1)
}
console.log(`✓ settings rows OK (${comps.size} components)`)
