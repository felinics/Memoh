<script setup lang="ts">
// Atoms: the simple, mostly-direct-render building blocks.
import { computed, ref } from 'vue'
import {
  Avatar, AvatarFallback, AvatarImage,
  Badge, BadgeCount,
  Button,
  Input,
  Kbd, KbdGroup,
  Label,
  SegmentedControl,
  Separator,
  Skeleton,
  Spinner,
  TextButton,
  TextGenerateEffect,
  Toggle,
} from '@felinic/ui'
import {
  ArrowRight,
  Bell,
  Bold,
  ChevronDown,
  Copy,
  ExternalLink,
  Inbox,
  Italic,
  Plus,
  RefreshCw,
  Strikethrough,
  Trash2,
  Underline,
} from 'lucide-vue-next'
import { SettingsIcon as Settings } from '@memohai/icon/ui'
import SectionShell from '../components/SectionShell.vue'
import Specimen from '../components/Specimen.vue'
import VariantMatrix from '../components/VariantMatrix.vue'
import { variantSpecs } from '../lib/variant-specs'

type PrimaryTone = 'neutral' | 'brand'
type Shape = 'rect' | 'pill'

const tone = ref<PrimaryTone>('neutral')
const shape = ref<Shape>('rect')
const primaryVariant = computed(() => tone.value === 'brand' ? 'brand' : 'default')
const primaryAliasVariant = computed(() => tone.value === 'brand' ? 'brand' : 'primary')
const shapeClass = computed(() => shape.value === 'pill' ? 'rounded-full' : undefined)

const toneOpts: { id: PrimaryTone, label: string }[] = [
  { id: 'neutral', label: 'Neutral' },
  { id: 'brand', label: 'Brand' },
]
const shapeOpts: { id: Shape, label: string }[] = [
  { id: 'rect', label: '8px rect' },
  { id: 'pill', label: 'Pill' },
]

const loading = ref<Record<string, boolean>>({})
function runLoad(key: string) {
  if (loading.value[key])
    return
  loading.value[key] = true
  setTimeout(() => { loading.value[key] = false }, 1600)
}

// Selected state demo — mirrors the real onboarding theme picker (Step2Appearance.vue),
// the one call site [data-ui-selected] actually replaced an injected bg-accent/border-foreground class.
const selectedTheme = ref<'light' | 'dark'>('light')

// Segmented control: now a REAL @felinic/ui component (the hand-rolled carryover
// that used to live here was productionized into SegmentedControl).
const range = ref('week')
const rangeItems = [
  { value: 'day', label: 'Day' },
  { value: 'week', label: 'Week' },
  { value: 'month', label: 'Month' },
]

// Toggle toolbars — exercise the real <Toggle> press model live (default = gray
// fill, tint = color-only).
const fmt = ref({ bold: true, italic: false, underline: false, strike: false })
const fmtTint = ref({ bold: true, italic: false, underline: false, strike: false })
</script>

<template>
  <SectionShell
    id="atoms"
    label="Atoms"
    description="Real primitives from @felinic/ui. The button card mirrors Controls contract so extraction can be judged in the same visual language."
  >
    <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <div class="lg:col-span-2">
        <div class="rounded-xl border border-border bg-card p-6">
          <div class="mb-5 flex flex-col gap-1">
            <code class="text-[11px] font-mono text-muted-foreground">&lt;Button&gt; contract set — real @felinic/ui</code>
            <span class="text-xs text-muted-foreground/75">Mirrors Controls contract: default=primary, outline=secondary, ghost=toolbar.</span>
          </div>
          <div class="mb-5 flex flex-wrap items-center gap-2 text-xs">
            <div class="flex items-center gap-1.5">
              <span class="text-muted-foreground">Primary tone</span>
              <div class="flex items-center gap-0.5 rounded-lg border border-border p-0.5">
                <button
                  v-for="t in toneOpts"
                  :key="t.id"
                  type="button"
                  class="rounded-md px-2 py-1 transition-colors"
                  :class="tone === t.id ? 'bg-accent font-medium text-foreground' : 'text-muted-foreground hover:bg-accent hover:text-foreground'"
                  @click="tone = t.id"
                >
                  {{ t.label }}
                </button>
              </div>
            </div>
            <div class="flex items-center gap-1.5">
              <span class="text-muted-foreground">Shape</span>
              <div class="flex items-center gap-0.5 rounded-lg border border-border p-0.5">
                <button
                  v-for="s in shapeOpts"
                  :key="s.id"
                  type="button"
                  class="rounded-md px-2 py-1 transition-colors"
                  :class="shape === s.id ? 'bg-accent font-medium text-foreground' : 'text-muted-foreground hover:bg-accent hover:text-foreground'"
                  @click="shape = s.id"
                >
                  {{ s.label }}
                </button>
              </div>
            </div>
          </div>
          <div class="flex w-full flex-col gap-7">
            <div class="flex flex-col gap-3">
              <span class="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">Weight variants</span>
              <div class="flex flex-wrap items-center gap-3">
                <Button
                  :variant="primaryVariant"
                  :class="shapeClass"
                >
                  Save
                </Button>
                <Button
                  variant="outline"
                  :class="shapeClass"
                >
                  <Plus /> Add provider
                </Button>
                <Button
                  variant="ghost"
                  :class="shapeClass"
                >
                  <RefreshCw /> Refresh
                </Button>
                <Button
                  variant="destructive"
                  :class="shapeClass"
                >
                  <Trash2 /> Delete
                </Button>
                <Button variant="link">
                  Learn more
                </Button>
                <Button
                  variant="outline"
                  :class="shapeClass"
                  disabled
                >
                  Disabled
                </Button>
              </div>
            </div>

            <div class="flex flex-col gap-3 border-t border-border pt-6">
              <span class="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">Size variants · sm (h-8) / default (h-9) / lg (h-10)</span>
              <div class="flex flex-wrap items-end gap-3">
                <Button
                  :variant="primaryVariant"
                  :class="shapeClass"
                  size="sm"
                >
                  Small
                </Button>
                <Button
                  :variant="primaryVariant"
                  :class="shapeClass"
                >
                  Default
                </Button>
                <Button
                  :variant="primaryVariant"
                  :class="shapeClass"
                  size="lg"
                >
                  Large
                </Button>
              </div>
              <div class="flex flex-wrap items-end gap-3">
                <Button
                  variant="outline"
                  :class="shapeClass"
                  size="sm"
                >
                  <Plus /> Small
                </Button>
                <Button
                  variant="outline"
                  :class="shapeClass"
                >
                  <Plus /> Default
                </Button>
                <Button
                  variant="outline"
                  :class="shapeClass"
                  size="lg"
                >
                  <Plus /> Large
                </Button>
              </div>
            </div>

            <div class="flex flex-col gap-3 border-t border-border pt-6">
              <span class="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">Icon buttons · ghost = toolbar, outline = standalone</span>
              <div class="flex flex-wrap items-end gap-6">
                <div class="flex flex-col gap-2">
                  <span class="text-[10px] text-muted-foreground">Ghost (toolbar)</span>
                  <div class="flex items-end gap-1.5">
                    <Button
                      variant="ghost"
                      :class="shapeClass"
                      size="icon-sm"
                      aria-label="Settings"
                    >
                      <Settings />
                    </Button>
                    <Button
                      variant="ghost"
                      :class="shapeClass"
                      size="icon"
                      aria-label="Refresh"
                    >
                      <RefreshCw />
                    </Button>
                    <Button
                      variant="ghost"
                      :class="shapeClass"
                      size="icon-lg"
                      aria-label="Copy"
                    >
                      <Copy />
                    </Button>
                  </div>
                </div>
                <div class="flex flex-col gap-2">
                  <span class="text-[10px] text-muted-foreground">Outline / secondary (standalone)</span>
                  <div class="flex items-end gap-1.5">
                    <Button
                      variant="outline"
                      :class="shapeClass"
                      size="icon-sm"
                      aria-label="Settings"
                    >
                      <Settings />
                    </Button>
                    <Button
                      variant="outline"
                      :class="shapeClass"
                      size="icon"
                      aria-label="Refresh"
                    >
                      <RefreshCw />
                    </Button>
                    <Button
                      variant="outline"
                      :class="shapeClass"
                      size="icon-lg"
                      aria-label="Copy"
                    >
                      <Copy />
                    </Button>
                  </div>
                </div>
              </div>
            </div>

            <div class="flex flex-col gap-3 border-t border-border pt-6">
              <span class="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">Icon position</span>
              <div class="flex flex-wrap items-center gap-3">
                <Button
                  variant="outline"
                  :class="shapeClass"
                >
                  <Plus /> Add item
                </Button>
                <Button
                  variant="outline"
                  :class="shapeClass"
                >
                  Next <ArrowRight />
                </Button>
                <Button
                  variant="outline"
                  :class="shapeClass"
                >
                  Options <ChevronDown />
                </Button>
                <Button variant="link-static">
                  Read guide
                </Button>
                <Button variant="link-draw">
                  View docs <ExternalLink />
                </Button>
              </div>
            </div>

            <div class="flex flex-col gap-3 border-t border-border pt-6">
              <span class="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">Alias check · these pairs should be visually identical</span>
              <div class="flex flex-wrap items-center gap-3">
                <Button :class="shapeClass">
                  default
                </Button>
                <Button
                  :variant="primaryAliasVariant"
                  :class="shapeClass"
                >
                  primary
                </Button>
                <Button
                  variant="outline"
                  :class="shapeClass"
                >
                  outline
                </Button>
                <Button
                  variant="secondary"
                  :class="shapeClass"
                >
                  secondary
                </Button>
              </div>
            </div>

            <div class="flex flex-col gap-3 border-t border-border pt-6">
              <span class="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">Loading state · click to load</span>
              <div class="flex flex-wrap items-center gap-3">
                <Button
                  :variant="primaryVariant"
                  :class="shapeClass"
                  :loading="loading.save"
                  loading-mode="overlay"
                  @click="runLoad('save')"
                >
                  Save changes
                </Button>
                <Button
                  variant="outline"
                  :class="shapeClass"
                  :loading="loading.sync"
                  loading-mode="icon"
                  @click="runLoad('sync')"
                >
                  <RefreshCw />
                  Sync
                </Button>
                <Button
                  variant="ghost"
                  :class="shapeClass"
                  size="icon"
                  aria-label="Refresh"
                  :loading="loading.refresh"
                  loading-mode="icon"
                  @click="runLoad('refresh')"
                >
                  <RefreshCw />
                </Button>
                <div class="w-72">
                  <Button
                    :variant="primaryVariant"
                    :class="shapeClass"
                    block
                    :loading="loading.continue"
                    loading-mode="leading"
                    @click="runLoad('continue')"
                  >
                    Continue
                  </Button>
                </div>
              </div>
            </div>

            <div class="flex flex-col gap-3 border-t border-border pt-6">
              <span class="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">Disabled state · 40% + no hover/press</span>
              <div class="flex flex-wrap items-center gap-3">
                <Button
                  :variant="primaryVariant"
                  :class="shapeClass"
                  disabled
                >
                  Primary
                </Button>
                <Button
                  variant="outline"
                  :class="shapeClass"
                  disabled
                >
                  Secondary
                </Button>
                <Button
                  variant="ghost"
                  :class="shapeClass"
                  disabled
                >
                  <RefreshCw /> Ghost
                </Button>
              </div>
            </div>

            <div class="flex flex-col gap-3 border-t border-border pt-6">
              <span class="text-caption font-medium uppercase tracking-wide text-muted-foreground">Selected state · [data-ui-selected] on outline (click to pick)</span>
              <div class="flex flex-wrap items-center gap-3">
                <Button
                  variant="outline"
                  :class="shapeClass"
                  :data-ui-selected="selectedTheme === 'light' ? '' : undefined"
                  @click="selectedTheme = 'light'"
                >
                  Light
                </Button>
                <Button
                  variant="outline"
                  :class="shapeClass"
                  :data-ui-selected="selectedTheme === 'dark' ? '' : undefined"
                  @click="selectedTheme = 'dark'"
                >
                  Dark
                </Button>
                <span class="text-caption text-muted-foreground">no injected class — fill/ring live on style.css chrome, same shell as hover/press</span>
              </div>
            </div>

            <div class="flex flex-col gap-3 border-t border-border pt-6">
              <span class="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">Segmented control · &lt;SegmentedControl&gt; @felinic/ui</span>
              <div class="flex flex-wrap items-center gap-4">
                <SegmentedControl
                  v-model="range"
                  :items="rangeItems"
                  aria-label="Range"
                />
                <span class="text-[11px] text-muted-foreground">→ {{ range }}</span>
              </div>
            </div>

            <div class="flex flex-col gap-3 border-t border-border pt-6">
              <span class="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">Link variants · fade / static / draw</span>
              <div class="flex flex-wrap items-center gap-4">
                <Button variant="link">
                  Learn more
                </Button>
                <Button variant="link-static">
                  Read guide
                </Button>
                <Button variant="link-draw">
                  View docs <ExternalLink />
                </Button>
              </div>
            </div>

            <div class="flex flex-col gap-3 border-t border-border pt-6">
              <span class="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">Text button · &lt;TextButton&gt; — clickable text with a hover chip (ghost @ text scale)</span>
              <div class="flex flex-wrap items-center gap-2">
                <TextButton>Rename</TextButton>
                <TextButton>
                  <Settings />
                  Settings
                </TextButton>
                <TextButton>
                  Appearance
                  <ChevronDown />
                </TextButton>
                <TextButton
                  as="a"
                  href="#atoms"
                >
                  Open as link
                </TextButton>
                <TextButton disabled>
                  Disabled
                </TextButton>
              </div>
            </div>

            <div class="flex flex-col gap-3 border-t border-border pt-6">
              <span class="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">Polymorphic anchor · button look on a real link</span>
              <div class="flex flex-wrap items-center gap-3">
                <Button
                  as="a"
                  href="#atoms"
                  variant="outline"
                  :class="shapeClass"
                >
                  Open docs <ExternalLink />
                </Button>
              </div>
            </div>
          </div>
        </div>
      </div>

      <Specimen label="<Badge :variant :size>">
        <VariantMatrix
          :variants="variantSpecs.badge.variants"
          :sizes="variantSpecs.badge.sizes"
          :exclude-sizes="{ outline: ['sm'] }"
        >
          <template #default="{ variant, size }">
            <Badge
              :variant="variant"
              :size="size"
            >
              {{ variant }}
            </Badge>
          </template>
        </VariantMatrix>
      </Specimen>

      <Specimen
        label="<BadgeCount :count>"
        note="a small solid count — destructive = the red alert dot (unread / needs attention), pinned to an icon corner. default = a neutral dot for plain informational counts that ride a tab / filter / segment label. In a flat list row a count is calmer as a plain muted numeral, no bubble (Inbox). Overflow caps at max (default 99)."
      >
        <div class="flex flex-wrap items-center gap-8">
          <!-- Canonical ALERT: red dot pinned to the corner of an icon button -->
          <span class="relative inline-flex">
            <Button
              variant="outline"
              size="icon"
              aria-label="Notifications"
            >
              <Bell />
            </Button>
            <BadgeCount
              :count="3"
              variant="destructive"
              class="absolute -right-1.5 -top-1.5"
            />
          </span>

          <!-- NEUTRAL home: real (interactive) filter buttons carrying their item
               count. "All" is a plain informational count (neutral); a filter only
               goes red when its count is itself an alert (unread mentions). -->
          <div class="flex items-center gap-1">
            <Button
              variant="ghost"
              size="sm"
            >
              All
              <BadgeCount :count="24" />
            </Button>
            <Button
              variant="ghost"
              size="sm"
            >
              Mentions
              <BadgeCount
                :count="5"
                variant="destructive"
              />
            </Button>
          </div>

          <!-- Flat list row: a count here is calmer as a plain muted numeral, no bubble -->
          <div class="flex w-40 items-center gap-2 text-body">
            <Inbox class="size-4 text-muted-foreground" />
            <span>Inbox</span>
            <span class="ml-auto text-caption tabular-nums text-muted-foreground">12</span>
          </div>

          <!-- Overflow caps — neutral (default) + alert (destructive) -->
          <div class="flex items-center gap-2">
            <BadgeCount :count="3" />
            <BadgeCount :count="42" />
            <BadgeCount
              :count="120"
              :max="9"
            />
            <BadgeCount
              :count="8"
              variant="destructive"
            />
            <BadgeCount
              :count="120"
              variant="destructive"
            />
          </div>
        </div>
      </Specimen>

      <Specimen
        label="<Avatar> image + fallback"
        note="Base is size-8 (32px) rounded-full; override the size per call site via class. Fallback shows when the image is missing/broken — neutral muted, never an accent tint."
      >
        <Avatar class="size-6">
          <AvatarImage
            src="https://avatars.githubusercontent.com/u/9919?v=4"
            alt="avatar"
          />
          <AvatarFallback>MH</AvatarFallback>
        </Avatar>
        <Avatar>
          <AvatarImage
            src="https://avatars.githubusercontent.com/u/9919?v=4"
            alt="avatar"
          />
          <AvatarFallback>MH</AvatarFallback>
        </Avatar>
        <Avatar class="size-10">
          <AvatarImage
            src="https://avatars.githubusercontent.com/u/9919?v=4"
            alt="avatar"
          />
          <AvatarFallback>MH</AvatarFallback>
        </Avatar>
        <Avatar class="size-10">
          <AvatarFallback>MH</AvatarFallback>
        </Avatar>
      </Specimen>

      <Specimen label="<Kbd> / <KbdGroup>">
        <Kbd>⌘</Kbd>
        <Kbd>Esc</Kbd>
        <KbdGroup>
          <Kbd>⌘</Kbd>
          <Kbd>K</Kbd>
        </KbdGroup>
      </Specimen>

      <Specimen label="<Separator> horizontal / vertical">
        <div class="w-full">
          <span class="text-xs text-muted-foreground">above</span>
          <Separator class="my-2" />
          <span class="text-xs text-muted-foreground">below</span>
        </div>
        <div class="flex h-8 items-center gap-2 text-xs text-muted-foreground">
          <span>A</span>
          <Separator orientation="vertical" />
          <span>B</span>
        </div>
      </Specimen>

      <Specimen
        label="<Skeleton>"
        note="One shimmer seam sweeps across all blocks in sync — every Skeleton samples the same viewport-anchored light, so a cluster reads as a single loading surface, not independent pulses."
      >
        <div class="flex w-full items-center gap-3">
          <Skeleton class="size-10 rounded-full" />
          <div class="flex flex-1 flex-col gap-2">
            <Skeleton class="h-3 w-2/3" />
            <Skeleton class="h-3 w-1/3" />
          </div>
        </div>
      </Specimen>

      <Specimen
        label="<Spinner>"
        note="rarely standalone — prefer a Button's own loading state (e.g. a Continue CTA) for action feedback; a bare Spinner is for full-pane / initial loads only. Inherits currentColor (size via class), never brand purple."
      >
        <Spinner />
        <Spinner class="size-6" />
        <Spinner class="size-8" />
        <Button
          variant="default"
          :loading="true"
          loading-mode="leading"
        >
          Continue
        </Button>
      </Specimen>

      <Specimen
        label="<Toggle> default · gray-ladder fill"
        note="rest → hover 249 → press 237 → on 243 → on+press 231"
      >
        <div class="flex items-center gap-0.5 rounded-lg border border-border p-0.5">
          <Toggle
            v-model:pressed="fmt.bold"
            size="sm"
            aria-label="Bold"
          >
            <Bold />
          </Toggle>
          <Toggle
            v-model:pressed="fmt.italic"
            size="sm"
            aria-label="Italic"
          >
            <Italic />
          </Toggle>
          <Toggle
            v-model:pressed="fmt.underline"
            size="sm"
            aria-label="Underline"
          >
            <Underline />
          </Toggle>
          <Toggle
            v-model:pressed="fmt.strike"
            size="sm"
            aria-label="Strikethrough"
          >
            <Strikethrough />
          </Toggle>
        </div>
      </Specimen>

      <Specimen
        label="<Toggle variant='tint'> color-only"
        note="active tints the icon; hover stays a calm gray"
      >
        <div class="flex items-center gap-0.5 rounded-lg border border-border p-0.5">
          <Toggle
            v-model:pressed="fmtTint.bold"
            variant="tint"
            size="sm"
            aria-label="Bold"
          >
            <Bold />
          </Toggle>
          <Toggle
            v-model:pressed="fmtTint.italic"
            variant="tint"
            size="sm"
            aria-label="Italic"
          >
            <Italic />
          </Toggle>
          <Toggle
            v-model:pressed="fmtTint.underline"
            variant="tint"
            size="sm"
            aria-label="Underline"
          >
            <Underline />
          </Toggle>
          <Toggle
            v-model:pressed="fmtTint.strike"
            variant="tint"
            size="sm"
            aria-label="Strikethrough"
          >
            <Strikethrough />
          </Toggle>
        </div>
      </Specimen>

      <Specimen
        label="<Toggle> disabled"
        note="a toggle lives in a toolbar — disabled = unavailable in place (off or on), dimmed and unpressable, not a standalone gray pill"
      >
        <div class="flex items-center gap-0.5 rounded-lg border border-border p-0.5">
          <Toggle
            size="sm"
            aria-label="Bold"
          >
            <Bold />
          </Toggle>
          <Toggle
            disabled
            size="sm"
            aria-label="Italic (unavailable)"
          >
            <Italic />
          </Toggle>
          <Toggle
            disabled
            :default-pressed="true"
            size="sm"
            aria-label="Underline (locked on)"
          >
            <Underline />
          </Toggle>
        </div>
      </Specimen>

      <Specimen
        label="<Label>"
        note="a real form label: clicking it focuses the bound control (for=) and screen readers announce the two as one field — not just styled text"
      >
        <div class="flex flex-col gap-1.5">
          <Label for="atoms-demo-input">Display name</Label>
          <Input
            id="atoms-demo-input"
            placeholder="Click the label above to focus me"
          />
        </div>
      </Specimen>

      <div class="lg:col-span-2">
        <Specimen label="<TextGenerateEffect :words>">
          <TextGenerateEffect words="Animated word-by-word reveal." />
        </Specimen>
      </div>
    </div>
  </SectionShell>
</template>
