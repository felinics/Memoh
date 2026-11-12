#!/usr/bin/env node
// Settings row guard — keeps apps/web on the settings layout contract from
// packages/ui (see "Settings list rhythm" in packages/ui/src/style.css):
//
//   · the card shell (SettingsSection) owns the horizontal inset
//     (--settings-inset, 16px);
//   · the list (SettingsSection, or SettingsList outside a card) owns the row
//     rhythm: min height, vertical padding, hairline between rows;
//   · a row only lays out its content; card-bleed primitives (SettingsBleed,
//     ModelListRow) pull back out by --settings-inset and require a card.
//
// Three mistakes break that contract silently. Type checks, eslint and
// check-ui-contract cannot see them, because all three are about WHERE a node
// sits in the rendered tree, not about the classes on one line:
//
//   ROW OUTSIDE A LIST — a settings row rendered with no list ancestor gets no
//     height, padding or hairline, so neighbouring rows collapse into each other
//     (e.g. rows placed straight into a DialogBody instead of a SettingsList, or
//     into SettingsSection's #actions / #footer slots).
//   HAND INSET IN A CARD — a block inside a SettingsSection card that still pads
//     or offsets itself by the card's own inset (px-4 / mx-4 / -mx-4 /
//     --settings-inset) is indented twice, or pulled past the card edge.
//   BLEED OUTSIDE A CARD — SettingsBleed or ModelListRow placed in a
//     SettingsList or bare surface pulls -mx-(--settings-inset) where nothing
//     supplied that inset, overflowing by 16px on each side.
//
// METHOD. Every .vue template under apps/web/src is parsed with the
// vue/compiler-sfc that apps/web already depends on, and local .vue template
// usages are followed so a component's own template is judged where it is
// rendered. Per component the guard derives, to a fixed point:
//   · rootRows   — rows at its root, so every usage must sit inside a list;
//   · rootInsets — card-inset classes at its root, an error inside a card;
//   · rootBleeds — card-bleed nodes at its root, an error outside a card;
//   · slots      — per slot name, the merged list/card context across outlets.
// Teleported overlay surfaces (DialogContent, DialogPanel, PopoverContent, …)
// render on <body>, resetting both list and card context while leaving in-place
// triggers in the caller's context. Components that no template renders as a
// tag (routed pages and <component :is> tabs) have no list or card above them.
//
// ESCAPE HATCH. A template comment directly before the element:
//   <!-- ui-allow-loose-row: <why this row needs no list rhythm> -->
//   <!-- ui-allow-card-inset: <why this block offsets by the card inset> -->
// The reason is required. Use it for a deliberate layout (a single row alone
// in a dialog body), not to silence a real collapse.
//
// Usage: node scripts/check-settings-rows.mjs [src-dir]   (default apps/web/src)
import { readdirSync, readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const { parse } = createRequire(join(ROOT, 'apps/web/package.json'))('vue/compiler-sfc')

const NODE_ELEMENT = 1
const NODE_COMMENT = 3
const ATTRIBUTE = 6
const DIRECTIVE = 7

const LIST_TAGS = new Set(['SettingsSection', 'SettingsList'])
// Only overlay surfaces that teleport their children to <body> reset context;
// Root / Provider / Trigger tags render their children in place.
const PORTAL = /^(?:(?:Dialog|AlertDialog|Sheet|Popover|DropdownMenu|DropdownMenuSub|ContextMenu|ContextMenuSub|Tooltip|HoverCard|Drawer|Select)(?:Content|ScrollContent|Panel|Portal)|CommandDialog|ConfirmDeleteDialog|Teleport)$/
// Library shells whose #trigger slot stays in place while body/default slots teleport.
const TRIGGER_ONLY_OVERLAY = new Set(['ConfirmPopover', 'FormDialogShell'])
const PORTAL_CTX = { list: 'no', card: 'no', row: false, bleed: false }

// Classes that offset a block by the card's own inset. Smaller values (an icon
// gap, an input's room for its adornment) are content layout, not the inset.
const CARD_INSET = /(?:^|[\s'"`:{,])(-?(?:mx|ml|mr|ms|me|px|pl|pr|ps|pe|p)-(?:4|\(--settings-inset\)))(?=$|[\s'"`},])/g
// A block with its own surface (a full border, a radius, a fill) pads itself
// for its own content; that padding is not the card's inset. A one-sided
// hairline (border-b / border-t, with its border-border colour) is a row-like
// divider, not a surface, so it does not exempt the block.
const OWN_SURFACE = /(?:^|[\s'"`])(?:border|rounded(?:-[\w[\]().-]+)?|bg-(?!transparent)[\w/[\]().-]+)(?=$|[\s'"`])/

const pascal = tag => tag.replace(/-([a-z])/g, (_, c) => c.toUpperCase()).replace(/^[a-z]/, c => c.toUpperCase())
const attr = (node, name) => node.props?.find(p => p.type === ATTRIBUTE && p.name === name)?.value?.content
const bound = (node, name) => node.props?.find(p => p.type === DIRECTIVE && p.name === 'bind' && p.arg?.content === name)?.exp?.content
const classes = node => `${attr(node, 'class') ?? ''} ${bound(node, 'class') ?? ''}`

const isListRow = (tag, node) =>
  tag === 'SettingsRow' || tag === 'ExpandableSettingsRow' || (tag === 'InlineLoadingRow' && attr(node, 'surface') === 'card-row')

const isCardBleed = tag => tag === 'SettingsBleed' || tag === 'ModelListRow'

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
  if (!dir || !dir.arg) return 'default'
  return dir.arg.isStatic === false ? null : dir.arg.content
}

const mergeList = (a, b) => (a === b ? a : (a === 'no' || b === 'no') ? 'no' : 'inherit')

function mergeSlot(prev, ctx) {
  const list = ctx.list === 'root' ? 'inherit' : ctx.list
  const card = ctx.card === 'root' ? 'inherit' : ctx.card
  if (!prev) return { list, cards: [card], row: ctx.row, bleed: ctx.bleed }
  return {
    list: mergeList(prev.list, list),
    cards: [...new Set([...prev.cards, card])].sort(),
    row: prev.row && ctx.row,
    bleed: prev.bleed && ctx.bleed,
  }
}

function resolveSlotCtx(slot, here) {
  if (!slot) return here
  const list = slot.list === 'inherit' ? here.list : slot.list
  const cards = [...new Set(slot.cards.map(c => (c === 'inherit' ? here.card : c)))]
  const card = cards.length === 1 ? cards[0] : 'mixed'
  const inherits = slot.list === 'inherit' || slot.cards.includes('inherit')
  return {
    list,
    card,
    row: slot.row || (inherits && here.row),
    bleed: slot.bleed || (inherits && here.bleed),
  }
}

export function checkSettingsRows(srcDir = join(ROOT, 'apps/web/src')) {
  const src = resolve(srcDir)
  const rel = f => (relative(ROOT, f).startsWith('..') ? relative(src, f) : relative(ROOT, f))

  const files = []
  const walk = dir => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const p = join(dir, entry.name)
      if (entry.isDirectory()) walk(p)
      else if (p.endsWith('.vue')) files.push(p)
    }
  }
  walk(src)

  const comps = new Map()
  for (const file of files) {
    let descriptor
    try { descriptor = parse(readFileSync(file, 'utf8'), { filename: file }).descriptor }
    catch { continue }
    if (!descriptor.template?.ast) continue
    const imports = {}
    const script = (descriptor.scriptSetup?.content ?? '') + (descriptor.script?.content ?? '')
    for (const m of script.matchAll(/import\s+(\w+)\s*(?:,\s*\{[^}]*\})?\s+from\s+['"]([^'"]+\.vue)['"]/g)) {
      imports[m[1]] = m[2].startsWith('@/') ? join(src, m[2].slice(2)) : resolve(dirname(file), m[2])
    }
    comps.set(file, { ast: descriptor.template.ast, imports, slots: {}, rootRows: [], rootInsets: [], rootBleeds: [] })
  }

  // list: 'yes' | 'no' | 'root'; card: 'yes' | 'no' | 'mixed' | 'root'
  function analyze(file) {
    const comp = comps.get(file)
    const out = { slots: {}, rootRows: [], rootInsets: [], rootBleeds: [], rendered: new Set(), errors: [] }
    const where = node => `${rel(file)}:${node.loc.start.line}`

    const visit = (node, ctx, siblings, index) => {
      if (node.type !== NODE_ELEMENT) return
      if (node.tag === 'slot') {
        if (!bound(node, 'name')) {
          const name = attr(node, 'name') ?? 'default'
          out.slots[name] = mergeSlot(out.slots[name], ctx)
        }
        return
      }
      const tag = pascal(node.tag)
      const isPortal = PORTAL.test(tag)
      const here = isPortal ? PORTAL_CTX : ctx
      const childFile = comp.imports[node.tag] ?? comp.imports[tag]
      const child = comps.get(childFile)
      if (child) out.rendered.add(childFile)

      const looseAllowed = allowed(siblings, index, 'ui-allow-loose-row')
      const insetAllowed = allowed(siblings, index, 'ui-allow-card-inset')
      const bleedAllowed = insetAllowed || (tag === 'ModelListRow' && looseAllowed)
      const baseRow = isListRow(tag, node)
      const baseBleed = isCardBleed(tag)

      if (baseRow && !here.row && !looseAllowed) {
        if (here.list === 'root') out.rootRows.push(`${where(node)}  <${node.tag}>`)
        else if (here.list === 'no') out.errors.push(`${where(node)}  <${node.tag}> is a settings row outside any SettingsSection / SettingsList — it gets no row height, padding or hairline. Wrap the rows in SettingsList (inside a dialog body) or a SettingsSection.`)
      }
      if (child && !here.row && !looseAllowed) {
        if (here.list === 'no') {
          for (const row of child.rootRows) out.errors.push(`${row} is a settings row outside any SettingsSection / SettingsList (rendered by ${where(node)}) — it gets no row height, padding or hairline. Wrap the rows in SettingsList (inside a dialog body) or a SettingsSection.`)
        }
        else if (here.list === 'root') {
          for (const row of child.rootRows) out.rootRows.push(`${row} (via ${where(node)})`)
        }
      }

      if (baseBleed && !here.row && !here.bleed && !bleedAllowed) {
        if (here.card === 'no' || here.card === 'mixed') out.errors.push(`${where(node)}  <${node.tag}> bleeds by --settings-inset outside any SettingsSection card — in a SettingsList or bare surface nothing supplies that inset, so it overflows by 16px.`)
        else if (here.card === 'root') out.rootBleeds.push(`${where(node)}  <${node.tag}>`)
      }
      if (child && !here.row && !here.bleed && !bleedAllowed) {
        if (here.card === 'no' || here.card === 'mixed') {
          for (const bleed of child.rootBleeds) out.errors.push(`${bleed} bleeds by --settings-inset outside any SettingsSection card (rendered by ${where(node)}) — in a SettingsList or bare surface nothing supplies that inset.`)
        }
        else if (here.card === 'root') {
          for (const bleed of child.rootBleeds) out.rootBleeds.push(`${bleed} (via ${where(node)})`)
        }
      }

      const insetChecked = !here.row && !here.bleed && !baseBleed && !LIST_TAGS.has(tag) && !isPortal && !TRIGGER_ONLY_OVERLAY.has(tag)
      if (insetChecked && !insetAllowed) {
        const cls = classes(node)
        const hits = [...cls.matchAll(CARD_INSET)].map(m => m[1])
        if (hits.length && !OWN_SURFACE.test(cls)) {
          const msg = `<${node.tag}> ${hits.join(' ')}`
          if (here.card === 'yes' || here.card === 'mixed') out.errors.push(`${where(node)}  ${msg} inside a settings card — the card already insets its content by --settings-inset. Drop the horizontal inset (keep vertical padding), or wrap edge-to-edge content in SettingsBleed.`)
          else if (here.card === 'root') out.rootInsets.push(`${where(node)}  ${msg}`)
        }
      }
      if (child && !here.row && !here.bleed && !insetAllowed) {
        if (here.card === 'yes' || here.card === 'mixed') {
          for (const inset of child.rootInsets) out.errors.push(`${inset} inside a settings card (rendered by ${where(node)}) — the card already insets its content by --settings-inset.`)
        }
        else if (here.card === 'root') {
          for (const inset of child.rootInsets) out.rootInsets.push(`${inset} (via ${where(node)})`)
        }
      }

      if (TRIGGER_ONLY_OVERLAY.has(tag)) {
        node.children.forEach((c, i) => visit(c, slotOf(c) === 'trigger' ? here : PORTAL_CTX, node.children, i))
        return
      }
      if (tag === 'SettingsSection') {
        node.children.forEach((c, i) => {
          const name = slotOf(c)
          const slotCtx = name === 'actions'
            ? { list: 'no', card: 'no', row: false, bleed: false }
            : name === 'footer'
              ? { list: 'no', card: 'yes', row: false, bleed: false }
              : { list: 'yes', card: 'yes', row: false, bleed: false }
          visit(c, slotCtx, node.children, i)
        })
        return
      }
      if (child) {
        node.children.forEach((c, i) => {
          const name = slotOf(c)
          visit(c, name === null ? here : resolveSlotCtx(child.slots[name], here), node.children, i)
        })
        return
      }

      let next = here
      if (baseRow || tag === 'ModelListRow') next = { ...here, row: true }
      else if (tag === 'SettingsBleed') next = { ...here, bleed: true }
      else if (tag === 'SettingsList') next = { list: 'yes', card: here.card === 'yes' || here.card === 'mixed' ? 'mixed' : 'no', row: false, bleed: false }
      else if (node.tag === 'component') {
        const is = node.props?.find(p => p.type === DIRECTIVE && p.arg?.content === 'is')?.exp?.content ?? ''
        const hasSection = /\bSettingsSection\b/.test(is)
        const hasList = /\bSettingsList\b/.test(is)
        if (hasSection || hasList) {
          const hasPlain = /['"][a-z][\w-]*['"]/.test(is)
          if (hasPlain && !looseAllowed) {
            out.errors.push(`${where(node)}  <component :is="${is}"> renders a plain element on one branch, so the rows inside lose their list rhythm there. Use SettingsList for the bare branch.`)
          }
          const card = hasSection && !hasList && !hasPlain ? 'yes' : hasSection ? 'mixed' : 'no'
          next = { list: 'yes', card, row: false, bleed: false }
        }
      }

      node.children.forEach((c, i) => visit(c, next, node.children, i))
    }

    const top = comp.ast.children
    top.forEach((c, i) => visit(c, { list: 'root', card: 'root', row: false, bleed: false }, top, i))
    return out
  }

  for (let pass = 0; pass < 50; pass++) {
    let changed = false
    for (const [file, comp] of comps) {
      const r = analyze(file)
      if (
        JSON.stringify(r.slots) !== JSON.stringify(comp.slots)
        || r.rootRows.join() !== comp.rootRows.join()
        || r.rootInsets.join() !== comp.rootInsets.join()
        || r.rootBleeds.join() !== comp.rootBleeds.join()
      ) changed = true
      Object.assign(comp, { slots: r.slots, rootRows: r.rootRows, rootInsets: r.rootInsets, rootBleeds: r.rootBleeds })
    }
    if (!changed) break
  }

  const errors = new Set()
  const rendered = new Set()
  for (const file of comps.keys()) {
    const r = analyze(file)
    r.errors.forEach(e => errors.add(e))
    r.rendered.forEach(f => rendered.add(f))
  }

  // A component that no template renders as a <Tag> is mounted by the router or
  // via <component :is>; nothing above its root provides a list or a card.
  for (const [file, comp] of comps) {
    if (rendered.has(file)) continue
    for (const row of comp.rootRows) {
      errors.add(`${row} sits at the root of ${rel(file)}, which no template renders directly — nothing above it provides a list. Wrap the rows in SettingsSection or SettingsList.`)
    }
    for (const bleed of comp.rootBleeds) {
      errors.add(`${bleed} sits at the root of ${rel(file)}, which no template renders directly — nothing above it provides a SettingsSection card.`)
    }
  }

  return { components: comps.size, errors: [...errors].sort() }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const { components, errors } = checkSettingsRows(process.argv[2])
  if (errors.length) {
    console.error(`✗ settings rows: ${errors.length} problem(s)\n`)
    for (const e of errors) console.error(`  ${e}`)
    console.error('\nContract: packages/ui/skills/ui-owners/SKILL.md (SettingsSection / SettingsList / SettingsBleed).')
    process.exit(1)
  }
  console.log(`✓ settings rows OK (${components} components)`)
}
