<script setup lang="ts">
// Layout / structure.
import { ref } from 'vue'
import {
  Accordion, AccordionContent, AccordionItem, AccordionTrigger,
  ActionCard,
  Button,
  Collapsible, CollapsibleContent, CollapsibleTrigger,
  Dialog, DialogClose, DialogFooter, DialogHeader, DialogScrollContent, DialogTitle,
  Input, Label,
} from '@felinic/ui'
import {
  ArrowUpRight, ChevronsUpDown, Funnel, KeyRound, ListChecks, Palette, Route,
  ScrollText, Shield, ShieldCheck, SlidersHorizontal, UserCog,
} from 'lucide-vue-next'
import SectionShell from '../components/SectionShell.vue'
import Specimen from '../components/Specimen.vue'

const open = ref(false)

// Candidates for the Advanced-rules entry icon (bot-access.vue currently ships
// ShieldCheck). Chosen to avoid glyphs already carrying a different meaning
// elsewhere on the SAME page (bot-access.vue already uses Power/Info/RotateCcw/
// SquarePen/Trash2/Users — reusing one of those here would say "this does X"
// while meaning something else).
const advancedIconOptions = [
  { name: 'ShieldCheck', icon: ShieldCheck, note: 'current pick — access is verified/protected' },
  { name: 'Shield', icon: Shield, note: 'plainer access/protection, no checkmark' },
  { name: 'ListChecks', icon: ListChecks, note: 'leans on "a list of rules", not access itself' },
  { name: 'Funnel', icon: Funnel, note: 'rules as conditions that filter traffic' },
  { name: 'KeyRound', icon: KeyRound, note: 'permission/access-key framing' },
  { name: 'Route', icon: Route, note: 'scoped routing (platform/conversation) framing' },
  { name: 'UserCog', icon: UserCog, note: 'configuring who/how — closer to "manage members" than "rules"' },
  { name: 'ScrollText', icon: ScrollText, note: 'rules-as-document framing, more formal/legal-reading' },
]

// ── ActionCard presentation demo ─────────────────────────────────────────────
// ActionCard is presentation-agnostic — it only says "this is an action entry".
// The NEXT surface it opens is a real navigation (an in-place list<->detail
// slide swap that replaces the WHOLE right pane, or a focused Dialog) — neither
// can be honestly demoed at card scale inside a wall Specimen box without lying
// about the motion (a small box "morphing" reads nothing like a full-pane swap).
// So the wall only shows the card itself and its states; see bot-access.vue
// (Bot detail > Access > Channel tab > Advanced rules) for the real slide-swap
// wired end to end, and the Dialog demo below for the focused-form path.
const dialogOpen = ref(false)
const dialogName = ref('')
</script>

<template>
  <SectionShell
    id="layout"
    label="Layout"
    description="Structural primitives."
  >
    <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <Specimen label="<Collapsible>">
        <Collapsible
          v-model:open="open"
          class="w-full max-w-sm space-y-2"
        >
          <div class="flex items-center justify-between gap-2 rounded-md border border-border px-3 py-2">
            <span class="text-sm font-medium">Toggle details</span>
            <CollapsibleTrigger as-child>
              <Button
                variant="ghost"
                size="icon-sm"
              >
                <ChevronsUpDown />
              </Button>
            </CollapsibleTrigger>
          </div>
          <CollapsibleContent class="space-y-2">
            <div class="rounded-md border border-border px-3 py-2 text-xs text-muted-foreground">
              Hidden content row 1
            </div>
            <div class="rounded-md border border-border px-3 py-2 text-xs text-muted-foreground">
              Hidden content row 2
            </div>
          </CollapsibleContent>
        </Collapsible>
      </Specimen>

      <Specimen
        label="<Accordion type=single collapsible>"
        note="height tween · chevron flips · hairline row dividers"
      >
        <Accordion
          type="single"
          collapsible
          default-value="item-1"
          class="max-w-sm"
        >
          <AccordionItem value="item-1">
            <AccordionTrigger>What is a workspace runtime?</AccordionTrigger>
            <AccordionContent>
              An isolated workspace where a bot can edit files, run commands, and host tools.
            </AccordionContent>
          </AccordionItem>
          <AccordionItem value="item-2">
            <AccordionTrigger>Which channels are supported?</AccordionTrigger>
            <AccordionContent>
              Telegram, Discord, Lark, DingTalk, WeChat, Matrix, and more.
            </AccordionContent>
          </AccordionItem>
          <AccordionItem value="item-3">
            <AccordionTrigger>Can I run it locally?</AccordionTrigger>
            <AccordionContent>
              Yes — the desktop app connects to Memoh Cloud or a self-hosted server via MEMOH_DESKTOP_BASE_URL.
            </AccordionContent>
          </AccordionItem>
        </Accordion>
      </Specimen>
    </div>

    <!-- ── ActionCard ─────────────────────────────────────────────────────────
         A clickable ENTRY card: same white-card language, but leading icon +
         trailing chevron + whole-card hover mark it as "action / go somewhere",
         not a display box. Default shape is a SLIM single-line row (48px: a
         14px pad + the title's own line-height, no forced floor) — a bare
         size-4 icon, the same rung as the trailing chevron. A description, if
         supplied, still renders (one line, truncated) and grows the row past
         48px — that's honest content-driven height, not a bug; it is NOT
         floored to match a title-only row. -->
    <div class="mt-8 space-y-4">
      <div>
        <h3 class="text-label font-medium text-foreground">
          ActionCard
        </h3>
        <p class="mt-0.5 text-body text-muted-foreground">
          An entry point, not a display card. Hover the whole card. The next surface (a
          full-pane slide swap, or a focused dialog) is the caller's choice — see
          bot-access.vue's Advanced rules entry for the real slide swap in place.
        </p>
      </div>

      <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Specimen
          label="<ActionCard> — slim single-line default"
          note="48px: py-3.5 + the title's line-height, no forced floor — bare size-4 icon, same rung as the chevron"
        >
          <div class="w-full max-w-sm space-y-2">
            <ActionCard title="Advanced rules">
              <template #icon>
                <ShieldCheck />
              </template>
            </ActionCard>

            <ActionCard title="Resource limits">
              <template #icon>
                <SlidersHorizontal />
              </template>
            </ActionCard>
          </div>
        </Specimen>

        <Specimen
          label="<ActionCard description>"
          note="a description grows the row past 48px — natural content height, never floored to match a title-only peer"
        >
          <div class="w-full max-w-sm">
            <ActionCard
              title="Custom theme"
              description="Tap to open a focused dialog for rarely-touched settings."
            >
              <template #icon>
                <Palette />
              </template>
            </ActionCard>
          </div>
        </Specimen>
      </div>

      <!-- Icon shortlist for the Advanced-rules entry (bot-access.vue) — rendered
           at REAL scale (48px row, size-4 glyph) so the comparison is honest, not
           an isolated icon swatch guessing at final weight. Each is a candidate
           for "platform-/conversation-scoped rules beyond the member list" — not
           a decision, just options to compare before picking one. -->
      <Specimen
        label="Advanced-rules icon shortlist"
        note="same title, real 48px row — pick by how each glyph reads at actual weight, not in isolation"
      >
        <div class="grid w-full grid-cols-1 gap-2 sm:grid-cols-2">
          <div
            v-for="option in advancedIconOptions"
            :key="option.name"
          >
            <ActionCard title="Advanced rules">
              <template #icon>
                <component :is="option.icon" />
              </template>
            </ActionCard>
            <p class="mt-1 px-1 text-caption text-muted-foreground">
              <code class="font-mono">{{ option.name }}</code> — {{ option.note }}
            </p>
          </div>
        </div>
      </Specimen>

      <Specimen
        label="<ActionCard as='a' #trailing=ArrowUpRight>"
        note="external link: as='a' + href + ArrowUpRight trailing"
      >
        <div class="w-full max-w-sm">
          <ActionCard
            as="a"
            href="https://memoh.ai"
            target="_blank"
            rel="noopener"
            title="Open documentation"
          >
            <template #icon>
              <Palette />
            </template>
            <template #trailing>
              <ArrowUpRight class="size-4 shrink-0 text-muted-foreground" />
            </template>
          </ActionCard>
        </div>
      </Specimen>

      <!-- Focused-dialog presentation: opening a Dialog IS honest to demo at card
           scale (unlike a full-pane slide swap, a dialog's own scale doesn't
           depend on the surrounding pane), so this one is a real, working demo. -->
      <Specimen
        label="ActionCard -> focused Dialog"
        note="tap the card — opens the New Task dialog shape for a rarely-touched setting"
      >
        <div class="w-full max-w-sm">
          <ActionCard
            title="Custom theme"
            @click="dialogOpen = true"
          >
            <template #icon>
              <Palette />
            </template>
          </ActionCard>

          <Dialog v-model:open="dialogOpen">
            <DialogScrollContent class="sm:max-w-lg">
              <DialogHeader>
                <DialogTitle>Custom theme</DialogTitle>
              </DialogHeader>
              <form
                class="space-y-4"
                @submit.prevent="dialogOpen = false"
              >
                <div class="space-y-1.5">
                  <Label for="wall-theme-name">Preset name</Label>
                  <Input
                    id="wall-theme-name"
                    v-model="dialogName"
                    placeholder="My theme"
                  />
                </div>
                <DialogFooter class="gap-2">
                  <DialogClose as-child>
                    <Button
                      type="button"
                      variant="ghost"
                    >
                      Cancel
                    </Button>
                  </DialogClose>
                  <Button type="submit">
                    Save
                  </Button>
                </DialogFooter>
              </form>
            </DialogScrollContent>
          </Dialog>
        </div>
      </Specimen>
    </div>
  </SectionShell>
</template>
