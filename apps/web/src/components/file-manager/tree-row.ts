// The Explorer tree-row shape — ONE home for it.
//
// File-tree rows are a deliberately LOCAL row system (see
// packages/ui/skills/ui-owners/SKILL.md § "When to STAY hand-written": tree rows
// live on their own surface and are not settings rows). Local does not mean
// copied: the moment a second surface renders a tree row — the folder picker in
// the New-folder dialog — hand-copying these strings would be exactly the
// 同形异码 debt the owner skill warns about, invisible to grep and free to drift.
// So the shape lives here and every tree-row surface imports it; changing a value
// here moves the Explorer and the picker together.
//
// Chrome notes (why these specific escape hatches are correct here):
// - `hover:bg-[color:var(--sidebar-hover)]` is the sidebar surface's hover token,
//   the same one session-item.vue / folders-section.vue use. It is an app-scope
//   interaction fill only because this row system has no library owner.
// - `text-foreground/80` is the Explorer's resting ink, paired with the full
//   `text-foreground` a selected row gets.

/**
 * Geometry + type of one tree row: 27px pill, 4px side inset, Explorer type
 * scale (13.5px / 350 weight, no smoothing) — matched to VS Code's density, which
 * is why the numbers are literals rather than the settings spacing scale.
 */
export const treeRowClass = 'group/row flex min-h-[1.6875rem] cursor-pointer items-center mx-1 mb-px pl-1 pr-1 rounded-sm text-[0.84375rem] tracking-normal font-[350] select-none [-webkit-font-smoothing:auto]' /* ui-allow-px: Explorer row density is a fixed 27px/13.5px scale, not the settings spacing ladder */

/** A row that is the active/selected one. */
export const treeRowSelectedClass = 'bg-sidebar-accent text-foreground'

/** A row at rest: muted ink, sidebar hover fill. */
export const treeRowIdleClass = 'text-foreground/80 hover:bg-[color:var(--sidebar-hover)]' /* ui-allow-style: the Explorer row system has no library owner (ui-owners § "stay hand-written"); this is the shared sidebar hover token, and THIS module is its single home */ /* ui-allow-alpha: Explorer resting ink, paired with the full-strength text-foreground a selected row gets */

/** One depth step. Repeat `depth` times before the glyph slot. */
export const treeIndentClass = 'h-full w-2 shrink-0 self-stretch'

/** The fixed 24px column that holds a row's chevron or type glyph. */
export const treeGlyphSlotClass = 'flex size-6 shrink-0 items-center justify-center'

/** A non-row line rendered inside the tree (the picker's load-failed retry line). */
export const treeAsideClass = 'flex min-h-[1.6875rem] items-center mx-1 mb-px pl-1 pr-1 text-[0.84375rem] tracking-normal font-[350] text-muted-foreground [-webkit-font-smoothing:auto]' /* ui-allow-px: same Explorer row density as treeRowClass */
