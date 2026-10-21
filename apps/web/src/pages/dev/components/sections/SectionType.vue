<script setup lang="ts">
// Typography bench — the "type wall". Each scale step renders with its real
// `text-*` utility (generated from the --text-* tokens in style.css), then
// reads back the computed size / line-height / tracking on mount so the bench
// always reflects the source of truth. Tune a token → the whole app + this
// wall move together.
import { onMounted, ref, type ComponentPublicInstance } from 'vue'
import { Button } from '@felinic/ui'
import { RefreshCw } from 'lucide-vue-next'
import SectionShell from '../components/SectionShell.vue'
import Specimen from '../components/Specimen.vue'

// Button text-size A/B — judge in REAL button chrome, not in the abstract.
// Each row forces a font-size inline so it overrides text-control, letting us
// compare 12 / 13 / 14px side by side before committing to a token value.
const buttonSizes = [
  { px: 12, label: 'body (12px)' },
  { px: 13, label: 'label (13px)' },
  { px: 14, label: 'control (14px) — current' },
]

interface ScaleStep {
  /** token / utility name, e.g. `body` */
  name: string
  /** literal utility class (must be literal so Tailwind's scanner emits it) */
  cls: string
  /** what this step is for */
  use: string
}

const scale: ScaleStep[] = [
  { name: 'display', cls: 'text-display', use: 'Hero / empty-state' },
  { name: 'heading', cls: 'text-heading', use: 'Dialog / page heading' },
  { name: 'title', cls: 'text-title', use: 'Card / section / sheet title' },
  { name: 'control', cls: 'text-control', use: 'Buttons / compact titles' },
  { name: 'label', cls: 'text-label', use: 'Form labels / emphasized small' },
  { name: 'body', cls: 'text-body', use: 'Default UI body (workhorse)' },
  { name: 'caption', cls: 'text-caption', use: 'Badges / tiny meta' },
]

const weights = [
  { cls: 'font-normal', label: 'normal (400)' },
  { cls: 'font-medium', label: 'medium (500)' },
  { cls: 'font-semibold', label: 'semibold (600)' },
]

const colors = [
  { cls: 'text-foreground', label: 'foreground — primary copy' },
  { cls: 'text-muted-foreground', label: 'muted-foreground — secondary' },
  { cls: 'text-muted-foreground/60', label: 'muted/60 — disabled / placeholder' },
]

const sample = 'Memoh 排版 — The quick brown fox 0123456789'

// Measured specs, keyed by scale name.
const specs = ref<Record<string, string>>({})
const sampleRefs = ref<Record<string, HTMLElement | null>>({})

function setRef(name: string) {
  return (el: Element | ComponentPublicInstance | null) => {
    sampleRefs.value[name] = el as HTMLElement | null
  }
}

function measure() {
  const next: Record<string, string> = {}
  for (const step of scale) {
    const el = sampleRefs.value[step.name]
    if (!el) continue
    const cs = getComputedStyle(el)
    const px = (v: string) => `${Math.round(parseFloat(v) * 100) / 100}px`
    const tracking = cs.letterSpacing === 'normal' ? '0' : px(cs.letterSpacing)
    next[step.name] = `${px(cs.fontSize)} · lh ${px(cs.lineHeight)} · ls ${tracking}`
  }
  specs.value = next
}

onMounted(measure)
</script>

<template>
  <SectionShell
    id="type"
    label="Typography"
    description="The type scale, weights, and text-color ramp. Every sample renders with its real token-backed utility, so tuning a --text-* / color token in style.css updates this wall and the whole app at once."
  >
    <div class="grid grid-cols-1 gap-4">
      <!-- Type scale ladder -->
      <Specimen
        label="Type scale — text-{display,heading,title,body,label,caption}"
        note="size · line-height · letter-spacing read back from computed styles"
      >
        <div class="flex w-full flex-col divide-y divide-border">
          <div
            v-for="step in scale"
            :key="step.name"
            class="flex items-baseline gap-4 py-3"
          >
            <div class="w-24 shrink-0">
              <code class="text-[11px] font-mono text-muted-foreground">text-{{ step.name }}</code>
            </div>
            <div class="min-w-0 flex-1">
              <p
                :ref="setRef(step.name)"
                :class="[step.cls, 'truncate text-foreground']"
              >
                {{ sample }}
              </p>
            </div>
            <div class="w-56 shrink-0 text-right">
              <code class="text-[10px] font-mono text-muted-foreground/70">{{ specs[step.name] ?? '—' }}</code>
              <div class="text-[10px] text-muted-foreground/60">
                {{ step.use }}
              </div>
            </div>
          </div>
        </div>
      </Specimen>

      <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <!-- Weight ladder -->
        <Specimen
          label="Weight ladder — at text-body"
          note="weight stays orthogonal to size"
        >
          <div class="flex w-full flex-col gap-2">
            <div
              v-for="w in weights"
              :key="w.cls"
              class="flex items-baseline justify-between gap-4"
            >
              <p :class="`text-body ${w.cls} text-foreground`">
                {{ sample }}
              </p>
              <code class="shrink-0 text-[10px] font-mono text-muted-foreground/70">{{ w.label }}</code>
            </div>
          </div>
        </Specimen>

        <!-- Color ramp -->
        <Specimen
          label="Text-color ramp"
          note="one warm-neutral oklch ramp"
        >
          <div class="flex w-full flex-col gap-2">
            <div
              v-for="c in colors"
              :key="c.cls"
              class="flex items-baseline justify-between gap-4"
            >
              <p :class="`text-body ${c.cls}`">
                {{ sample }}
              </p>
              <code class="shrink-0 text-[10px] font-mono text-muted-foreground/70">{{ c.label }}</code>
            </div>
          </div>
        </Specimen>
      </div>

      <!-- Button text-size A/B in real chrome -->
      <Specimen
        label="Button text size — A/B in real chrome"
        note="same buttons forced to 12 / 13 / 14px so you can judge, not guess. Current = 14px (control)."
      >
        <div
          class="grid w-full items-center justify-items-start gap-x-6 gap-y-3"
          style="grid-template-columns: auto repeat(4, max-content)"
        >
          <template
            v-for="b in buttonSizes"
            :key="b.px"
          >
            <code class="text-[10px] font-mono text-muted-foreground/70">{{ b.label }}</code>
            <Button :style="{ fontSize: b.px + 'px' }">
              Save
            </Button>
            <Button
              variant="secondary"
              :style="{ fontSize: b.px + 'px' }"
            >
              Add provider
            </Button>
            <Button
              variant="ghost"
              :style="{ fontSize: b.px + 'px' }"
            >
              <RefreshCw />
              Refresh
            </Button>
            <Button
              variant="primary"
              :style="{ fontSize: b.px + 'px' }"
            >
              Continue
            </Button>
          </template>
        </div>
      </Specimen>

      <!-- Real-world composition -->
      <Specimen
        label="In context — title / body / caption stacked"
        note="how the steps read together"
      >
        <div class="flex w-full max-w-md flex-col gap-1.5 rounded-lg border border-border bg-card p-4">
          <h3 class="text-title font-semibold text-foreground">
            Workspace runtime
          </h3>
          <p class="text-body text-muted-foreground">
            Each bot runs in an isolated workspace runtime for editing files, executing
            commands, and building itself.
          </p>
          <span class="text-caption text-muted-foreground/60">Updated 2 minutes ago</span>
        </div>
      </Specimen>
    </div>
  </SectionShell>
</template>
