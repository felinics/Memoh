# Spacing Owner Vocabulary — Census Result

> This file is the only spacing research document kept in `docs/design/spacing/`. The
> other 18 process artifacts (cartography, raw audit batches, historical contract
> drafts) and the `memoh-spacing` skill were removed from HEAD in 2026-07 — their value
> was already captured in the vocabulary and decisions recorded below; git history has
> the rest if it's ever needed.

Date: 2026-07-01 · **Final status updated: 2026-07-06**

Status: **DONE — vocabulary complete, all known debt migrated.** Everything below this
block is the historical decision record (Phase-1 gap map → Phase-3 outcome), kept intact.
The terminal state as of 2026-07-06:

- **Settings vocabulary (11 owners)** ships and is in use: PageShell, SettingsSection
  (+`#footer`), SettingsRow (`stack: never|sm|always`, `align`, `#leading`/`#content`),
  ExpandableSettingsRow, BackendCard, ModelListRow (2026-07-06: the dense clickable
  model-row family, see Known remainder), FieldStack (**v2 — carries vee-validate
  validation state**), FormStack, MetricReadout, PersonaTile, CalloutBanner (+ the
  `Empty` atom).
- **Non-settings vocabulary added (2026-07-04/05)** from the non-settings sweep: PanePlaceholder,
  InlineLoadingRow, SectionGroup, SidebarPanelHeader, SidebarNavButton, DockPanelFrame,
  ConfirmDeleteDialog, and the onboarding wizard family (StepExitShell / StepFrame /
  FooterNav / ChoiceTile / HintBox). The building contract for all of these is the
  UI submodule's layout-owner guidance.
- **Three audits closed all reachable debt, and a post-review residual pass (2026-07-06)
  drained what they missed.** The Phase-3 census (35 settings files) → 21 files migrated.
  The **click-surface audit** (40 dialog/popover files) →
  36 MISS, all migrated (group-1 plain owners; FieldStack v2 built to absorb the validated
  fields; group-2 dialogs migrated onto it). The **coverage-closure sweep**
  (all 282 apps/web `.vue`, 0 skimmed) → 53 more MISS, migrated.
  The **non-settings sweep** then covered the surfaces the settings vocabulary never owned
  (sidebar, dockview, onboarding, chat loaders). The residual pass caught the sites that
  slipped every earlier net — mostly loading shapes judged "correctly local" *before*
  InlineLoadingRow/PanePlaceholder existed (2026-07-04) and never re-judged: supermarket
  list/detail loaders, bot-compaction's settings loader, file-tree,
  browser-pane/panel-files placeholders, appearance's code-highlight row (its mermaid
  sibling was TRIED on SettingsRow and reverted after visual review — it's a three-piece
  row whose full-width preview the owner can't model; reason recorded in-file), and
  keyboard-shortcuts'
  hand page shell — the corrections to each stale verdict are captured in the list above.
- **A regression backstop shipped, then hardened.** `check-ui-contract.mjs` rule 11 (WARN)
  flags `min-h-[3.75rem]` — and its bare-scale twin `min-h-15` — outside the owner files;
  the `ui-allow-shape` escape now requires a written reason on the marker line. Rule 12
  ratchets bare `animate-spin` (a loader that skipped the four-rung ladder) via
  `ui-spin-baseline.json` — new hand-spun loaders hard-fail. These catch the SettingsRow
  and loader slices (the rare literals); they are backstops, not debt finders. WARN count
  is held at **zero**.
- **The building contract lives in the UI submodule's layout-owner guidance.** The older `memoh-spacing`
  skill (the cartography/role-taxonomy method that derived this vocabulary) has been
  deleted from the repo (2026-07); its history is recoverable from git if the research
  trail is ever needed again.

### Known remainder (deliberately not migrated)

- **`pages/providers/model-setting.vue` identity header** — no current owner cleanly fits
  (avatar + truncated title + trailing controls in a shape none of the owners cover). Left local;
  revisit only if a second instance of the exact shape appears (then it earns an owner).
- **Chat tool-call detail layer — no longer deferred.** The 5 surface-scoped owners
  (EmptyRow / PreviewBox / HeaderRow / ExpandChevron / Capsule, `pages/home/components/
  tool-detail/`) shipped in `4af8c38ee` and are adopted across every `tool-call-detail-*`
  panel. What stays deferred to the chat UI revamp is the chat *surface* beyond the
  detail layer — message rows, composer, and the round chat-pane buttons listed below.
- **Spinner→`Button :loading` long tail — drained to the deferred chat surface (2026-07-06).**
  The settings/onboarding/about slices adopted `:loading` (Step4Bot, settings-acp-detail,
  mcp-server-detail, session-info-panel, about — mutual-lock `:disabled` semantics preserved
  where the loading boolean wasn't the whole story), and guard rule 12 now ratchets bare
  `animate-spin` so the tail can't regrow. What remains lives in `ui-spin-baseline.json`
  (14 sites, 12 files): the deferred chat surface (chat-pane, bg-task-pill,
  background-task-block, tool-call-detail-*) plus verified bare-glyph rungs (input-affix
  checks, tree-node loaders, status badges) that no owner should absorb.
- **Round icon buttons in chat-pane** (×6) — need a design call; other round-button
  stay-locals carry head comments recording their reasons.
- **Dense model-list navigation rows — RESOLVED, owner built.** The 3-file family
  (`transcription/provider-setting.vue`, `speech/components/provider-setting.vue`,
  `video/provider-setting.vue`) hand-synced the same clickable navigation-row shape:
  transcription and speech were byte-identical; video only differed by nesting the click
  handler on a trailing ghost Button instead of making the whole row a `<button>` — a
  trivial divergence, not a deliberate one, per the original audit's read. Built
  `ModelListRow` (`apps/web/src/components/settings/model-list-row.vue`, whole-row
  `<button>`, same root contract as BackendCard) and migrated all three onto it.
  **`providers/model-item.vue` stays separate, deliberately** — it carries inline
  enable/test/delete actions, a capability badge, and a status line, a materially richer
  interaction contract than "click to open the edit dialog," so forcing it onto the same
  owner would have been unifying two different relationships, not one. Recorded in
  the UI submodule guidance (Rows section + decision map).

---

The gap map below is the historical decision record (kept intact). What actually shipped is
recorded in the "Phase 3 outcome" section immediately after this note. All six owner gaps
were built, verified, and mass-applied; the vocabulary is now complete and in use.

## Phase 3 outcome (what shipped)

All six owners were built + a seventh capability was added, then applied across 21
settings/config files in four commits (`29112a7a2`, `0f92db4c1`, `0542733b6`,
`521d2ff5f`). Final gate: ESLint 0, vue-tsc 0 errors, UI contract guard 0 new
violations (the 9 remaining live in `sidebar/index.vue` + `video/provider-setting.vue`,
pre-existing debt outside this work).

**Owners built:** FieldStack (+FormStack), MetricReadout, SettingsSection `#footer`
slot, ExpandableSettingsRow, PersonaTile, CalloutBanner — plus **SettingsRow gained a
`stack: 'never'|'sm'|'always'` prop**. `stack='always'` (permanent column: a full-width
control under its label) was the real closer for schema-driven multi-line fields;
`bot-network`'s two same-shape-different-code field loops (primary + advanced) collapsed
onto it keyed off `isMultilineField`.

**Migrated (21 files):** batch 1 — bot-email, bot-plugins, bot-user-access,
settings-danger-zone, ShortcutRow, provider-form, provider-setting, usage/index
(+ chart-card DELETED as a byte-copy of SettingsSection). batch 2 — bot-compaction,
bot-memory, bot-tool-approval. batch 3 — schedule-editor, password-section,
model-config-editor, schedule-pattern-builder, channel-field, bot-skills, bot-container.
By hand (too complex/dynamic for a batch) — bot-network, bot-access.

**Two census mis-buckets corrected by reading real structure at migration time:**
- `backup-section-cards` was bucketed as an ExpandableSettingsRow occurrence "by expand
  chevron"; its real morphology is a dense multi-state selection card (strategy segmented
  control + include checkbox + `text-[10px]` mono detail list). Stays local.
- The `SchemaFieldRow` pseudo-gap (rejected below) was the right call, but the reason
  sharpened: it's not a new owner, it's `SettingsRow stack='always'` — which we built.

**Stays-local confirmed at migration time (not laziness — genuinely different relationships):**
web-search settings ×19 were already pure `SettingsRow` (no work); `bot-acp`
stretched-overlay row (BackendCard root is a `<button>`, can't nest a Switch);
`schedule-list-item` shared component with a DropdownMenu in trailing + a sidebar variant;
`channel-settings-panel` domain `ChannelField` + manual-toggle-of-sibling; `bot-import-panel`
+ `bot-backup-actions` dialog-body compounds with their own denser language; `bot-access`
inline rule form (5 fields with `text-xs` muted labels + `h-8` inputs + inline clear
buttons — a tighter language than a settings-page FieldStack).

---

_Everything below is the original Phase-1 gap map, preserved as the decision record._

Built from a 125-sample morphology census that read the
FULL template structure of 35 settings/config files (not grep). This file answers the
one question the earlier docs never closed: **is the owner vocabulary complete enough
to stop defining and start batch-applying?**

Answer: **~60% complete.** Four load-bearing owners already ship the exact slots
needed. Six new owners must be built before mass migration. Everything else stays local.

## The Phase Model (why this doc exists)

```
Phase 1  Define + build the FULL owner vocabulary   ← WE ARE HERE (6 owners left)
Phase 2  Pilot-verify each owner on ONE real page   ← done for SettingsRow (bot-overview, profile)
Phase 3  Mass-apply via subagent batch (V1 packet)  ← NOT a page-by-page main-thread edit
```

The mistake that produced this doc: after piloting `SettingsRow` on two pages, work
drifted into "migrate the next page, and the next" — applying Phase-3 tactics (page by
page) while still in Phase 1 (vocabulary incomplete). You cannot mass-migrate onto a
half-defined vocabulary: the undefined shapes get hand-rolled again *during* migration.
Finish the dictionary, then replace the book.

## Existing owners — READY for mass migration now

These four already ship the exact API the census assumed was pending. ~54 occurrences
are migratable today.

| Morphology | Owner | Occ | Files | Note |
|---|---|---:|---:|---|
| horizontal 3-seg row | `SettingsRow` | 24 | 15 | `#leading` / `#content` / default trailing all shipped |
| dense object-list row | `SettingsRow` | 11 | 8 | needs one optional `align?: 'center'\|'start'` prop (2-3 items-start cases) |
| section card wrapper | `SettingsSection` | 8 | 7 | `chart-card.vue` is a BYTE-COPY → delete + repoint |
| empty state | `Empty` (existing) | 11 | 8 | only fold `py-12/16` into an Empty padding default |
| page column | `PageShell` | 2 | — | `variant:'tab'\|'page'` already ships the exact column |

## Owner gaps — BUILD these six before mass migration (ranked by leverage)

Each: build once in the shared layer, verify against the first-validation-target page,
then it unblocks its bucket.

### 1. FieldStack (+ FormStack wrapper) — unblocks 13
- **First validation:** `bots/components/settings-acp-detail.vue` (ACP managed-field v-for)
- **API:** `FieldStack { label; for?; help? }`, default slot = the control. Renders
  `<div class="space-y-1.5"><Label :for/> + <slot/> + optional help <p class="text-xs text-muted-foreground"/></div>`.
  `FormStack` = `<div class="space-y-4">` wrapper for a run of FieldStacks.
- **Distinct from SettingsRow** (horizontal label|control). Do NOT force vertical
  Label-over-control clusters into SettingsRow. Highest-leverage gap.

### 2. MetricReadout — unblocks 9
- **First validation:** `bots/components/settings-context-card.vue` (repeats tile ~8x, incl. status-dot variants)
- **API:** `{ label; value?; sub?; framed?=true; status?: 'ok'|'warn'|'error' }`. Caller
  owns the grid. Framed tile = caption + tabular value (or status-dot+label) + optional sub,
  `min-h-[4.375rem]`. `framed:false` for bot-overview usage stats.

### 3. SettingsFooterBar — unblocks 7 (deliver as a SettingsSection `#footer` slot, NOT a standalone component)
- **First validation:** `email/components/provider-setting.vue` (LoadingButton submit)
- **API:** add `#footer` slot to `SettingsSection.vue`, rendered INSIDE the bordered card
  after the default slot: `<div v-if="$slots.footer" class="flex items-center justify-end border-t border-border py-3 px-4"><slot name="footer"/></div>`.
  `justify-between` when content spans (pagination). Trivial build, 6-file reuse, zero new component.

### 4. ExpandableSettingsRow — unblocks 6
- **First validation:** `dev/components/sections/SectionSpacing.vue` (interactive settings-row specimen)
- **API:** `{ label?; description? }` + `v-model:open` + slots `#leading` / `#content` /
  `#trailing` (default: chevron that rotates 90° on open) / `#expanded` (collapsible body).
  Internally composes `SettingsRow` for the header + a height/grid-rows transition.
  Simpler "Advanced" disclosures (network/channel/context-card) fall out for free.

### 5. PersonaTile (+ AddTile variant) — unblocks 5
- **First validation:** `bots/components/bot-card.vue` L2-49, then `bots/index.vue` create-tile + skeleton
- **API:** `{ name; variant?: 'entity'|'add' }`, slots `#media` (Avatar or plus-circle),
  `#status` (absolute corner badge). Vertical centered `w-52 flex-col items-center rounded border bg-card p-5`.
  `add` variant swaps `bg-card`→`bg-background`. **BackendCard does NOT cover this** — BackendCard is horizontal; this is vertical/centered.

### 6. CalloutBanner — unblocks 4
- **First validation:** `bots/components/bot-container.vue` L1035-1128 (3 stacked warning callouts share one skeleton)
- **API:** `{ tone: 'warning'|'destructive'; title; description?; clickable? }`, slots
  `#icon` (default AlertCircle) + default trailing action slot. Rounded `bg-{tone}-soft`
  framed row, `sm:flex-row` responsive. `clickable` → whole surface is a `<button>` with trailing ChevronRight (bot-overview BannerButton).

## Do NOT build these (rejected pseudo-gaps — keep the vocabulary narrow)

| Tempting owner | Why not | What covers it instead |
|---|---|---|
| `DenseListSection` | non-gap | `SettingsSection` `#actions` slot already carries the header toolbar |
| `FramedEmpty` | non-gap | extend existing `Empty` with `framed?` + action slot |
| `SchemaFieldRow` / `StatusActionRow` | non-gap | a responsive `stack?: 'always'\|'sm'` variant on `SettingsRow` |
| `DescriptionList` / `DefinitionRow` | defer (n=2) | keep local until a 3rd read-only key/value site appears |
| `DataTable` | n=1 | one genuine table (compaction logs) — keep local |

## Stays-local (deliberately hand-rolled, ~34 occ)

- centered placeholders (spinner/skeleton borrowing row min-height so panels don't reflow) — 22 occ
- genuinely one-off compound blocks (OAuth device-flow, link-code countdown, Monaco JSON dialog, snapshot input) — the non-footer part of 12
- trivial muted no-results `<p>` lines
- the single real data table

## Execution Order

1. **Build the six owners**, each verified against its first-validation-target page. Two
   quick wins first (SettingsSection `#footer` slot, SettingsRow `align`/`stack` props are
   prop-adds, not new components), then the four real new components.
2. Only after all six exist + are verified: assemble the **Phase-3 subagent batch** — a
   migration packet per page, applied in parallel by subagents, each returning the
   V1-packet report. NOT a main-thread page-by-page edit.
3. `mise run lint` + visual/measurement check per migrated page.

## Full bucket data

Raw census (125 samples, 6 readers + synthesizer) archived in the task output; the bucket
verdicts above are the durable result. Total: 4 existing + 6 new = the whole vocabulary;
everything else stays local.
