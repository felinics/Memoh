<template>
  <div
    v-if="shiki.loading.value"
    class="flex items-center gap-1.5 text-xs text-muted-foreground"
  >
    <Spinner class="size-3" />
  </div>
  <!-- The rows are rebuilt from shiki's <pre class="shiki"> output, so the
       design system's dark-mode override (.dark .shiki span) would no longer
       reach them — keep the shiki class on the container to stay inside that
       rule, or dark mode renders light-theme (near-black) token colors on the
       dark surface. -->
  <div
    v-else-if="shiki.diffRows.value.length > 0"
    class="shiki shiki-diff-grid overflow-auto max-h-96 text-xs font-mono leading-6"
  >
    <div class="diff-content">
      <!-- The panel sits flush in a rounded-sm card, and the indicator bar is
           flush with its left edge, so a changed row at the very top or bottom
           gets its bar clipped into a sliver by the corner arc (an edit at the
           start or end of a file). A spacer row of dashes there keeps every
           changed row off the corners. -->
      <div
        v-if="capTop"
        class="diff-row diff-cap"
        aria-hidden="true"
      >
        <span class="diff-ln">-</span>
        <span class="diff-mk" />
        <span class="diff-code text-muted-foreground">---</span>
      </div>
      <div
        v-for="(row, i) in shiki.diffRows.value"
        :key="i"
        class="diff-row"
        :data-kind="additionsOnly ? 'context' : row.kind"
      >
        <span class="diff-ln">{{ row.lineNumber }}</span>
        <span class="diff-mk">{{ additionsOnly ? '' : row.kind === 'remove' ? '−' : row.kind === 'add' ? '+' : '' }}</span>
        <!-- eslint-disable vue/no-v-html -->
        <span
          class="diff-code"
          v-html="row.html"
        />
        <!-- eslint-enable vue/no-v-html -->
      </div>
      <div
        v-if="capBottom"
        class="diff-row diff-cap"
        aria-hidden="true"
      >
        <span class="diff-ln">-</span>
        <span class="diff-mk" />
        <span class="diff-code text-muted-foreground">---</span>
      </div>
    </div>
  </div>
  <!-- eslint-disable vue/no-v-html -->
  <div
    v-else
    class="overflow-auto max-h-96 text-xs font-mono [&_pre]:bg-transparent! [&_pre]:p-2 [&_pre]:m-0 [&_code]:text-xs"
    v-html="shiki.html.value"
  />
  <!-- eslint-enable vue/no-v-html -->
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'
import { Spinner } from '@felinic/ui'
import { useShikiHighlighter } from '@/composables/useShikiHighlighter'

// Read-only diff panel shared by the edit, write, and apply_patch tool
// details. The server attaches a unified diff as UI-only metadata; rows
// render with gutter line numbers (old for removals, new for
// additions/context), −/+ markers, whole-row red/green bands with a solid
// indicator bar at the left edge, and an inline emphasis block on the exact
// replaced fragments.
const props = defineProps<{ diff: string, filename: string }>()
const shiki = useShikiHighlighter()

// A diff of additions only (a new file, or content written into an empty
// one) has nothing to compare against: every row would be green, so the
// colour, bar and + say nothing the title's +N does not. Those rows render as
// plain code instead.
const additionsOnly = computed(() => shiki.diffRows.value.every(row => row.kind === 'add'))

// Spacer rows only where a changed row's bar would otherwise touch a corner.
const capTop = computed(() => {
  const first = shiki.diffRows.value[0]
  return !additionsOnly.value && first !== undefined && first.kind !== 'context'
})
const capBottom = computed(() => {
  const last = shiki.diffRows.value.at(-1)
  return !additionsOnly.value && last !== undefined && last.kind !== 'context'
})

// Re-highlight whenever the diff arrives. Input now streams in after the tool
// block first renders (tool_call_input_start), so an onMounted-only highlight
// would miss content that lands later.
watch(
  () => [props.diff, props.filename] as const,
  ([diff, filename]) => {
    if (diff) {
      void shiki.highlightContextDiff(diff, filename)
    }
  },
  { immediate: true },
)
</script>

<style scoped>
/* Diff panel rows: a solid indicator bar at the left edge of changed rows,
   then gutter line number + −/+ marker + code, all sharing one horizontal
   scroll width so every row's band spans the same distance. The gutter
   columns stay sticky while scrolling; they repaint the band composited over
   the card surface (not --row-bg again, which would double the alpha into a
   brighter block) so the band reads flat while scrolled code stays hidden
   behind the gutter. */
.shiki-diff-grid .diff-content {
  width: max-content;
  min-width: 100%;
}
.shiki-diff-grid .diff-row {
  --row-bg: transparent;
  --inline-bg: transparent;
  position: relative;
  display: grid;
  grid-template-columns: 2rem 1.25rem max-content;
  background: var(--row-bg);
}
.shiki-diff-grid .diff-row[data-kind='remove'] {
  --row-bg: var(--diff-remove);
  --inline-bg: var(--diff-remove);
}
.shiki-diff-grid .diff-row[data-kind='add'] {
  --row-bg: var(--diff-add);
  --inline-bg: var(--diff-add);
}
/* The indicator bar lives inside the sticky line-number cell so it stays
   put during horizontal scroll, flush against the band's left edge. */
.shiki-diff-grid .diff-row[data-kind='remove'] .diff-ln::before,
.shiki-diff-grid .diff-row[data-kind='add'] .diff-ln::before {
  content: '';
  position: absolute;
  left: 0;
  top: 0;
  bottom: 0;
  width: 0.25rem;
}
.shiki-diff-grid .diff-row[data-kind='remove'] .diff-ln::before {
  background: var(--diff-remove-border);
}
.shiki-diff-grid .diff-row[data-kind='add'] .diff-ln::before {
  background: var(--diff-add-border);
}
.shiki-diff-grid .diff-ln,
.shiki-diff-grid .diff-mk {
  position: sticky;
  z-index: 1;
  background: linear-gradient(var(--row-bg), var(--row-bg)), var(--card);
  user-select: none;
}
.shiki-diff-grid .diff-ln {
  left: 0;
  padding-right: 0.375rem;
  text-align: right;
  color: var(--muted-foreground);
  font-size: 0.6875rem;
  font-variant-numeric: tabular-nums;
}
.shiki-diff-grid .diff-mk {
  left: 2rem;
  text-align: center;
}
.shiki-diff-grid .diff-row[data-kind='remove'] .diff-mk {
  color: var(--diff-remove-border);
}
.shiki-diff-grid .diff-row[data-kind='add'] .diff-mk {
  color: var(--diff-add-border);
}
.shiki-diff-grid .diff-code {
  padding: 0 0.75rem 0 0.375rem;
  white-space: pre;
  tab-size: 4;
}
.shiki-diff-grid .diff-code:empty::after {
  content: '\00a0';
}
/* The inline emphasis reuses the row band token: stacking the same alpha
   color over the band doubles its intensity, keeping light/dark themes in
   step without a second pair of tokens. The span is injected by shiki's
   v-html output, so it needs :deep to reach past the scoped-attribute
   boundary. */
.shiki-diff-grid .diff-code :deep(.diff-inline) {
  background-color: var(--inline-bg);
}
</style>
