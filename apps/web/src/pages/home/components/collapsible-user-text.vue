<script setup lang="ts">
import { nextTick, onMounted, ref, useTemplateRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useResizeObserver } from '@vueuse/core'

// A long user prompt collapses to ~11 lines with a quiet text toggle in the
// 12th line's place; expanded it shows everything plus "show less". The toggle
// is plain text (no button chrome), darkening to foreground on hover — it is an
// inline affordance on the bubble, not a control.
const props = defineProps<{ text: string }>()
const { t } = useI18n()

const expanded = ref(false)
const overflowing = ref(false)
const textEl = useTemplateRef<HTMLElement>('textEl')
const contentEl = useTemplateRef<HTMLElement>('contentEl')

// Whether the full text exceeds the collapsed height is a property of the text
// at the current width, so it is only meaningful while clamped — when expanded
// the clamp is gone and scrollHeight === clientHeight. Measure on mount, on
// text change, on collapse, and on width change (resizable pane).
function measure() {
  const el = textEl.value
  if (!el || expanded.value) return
  overflowing.value = el.scrollHeight - el.clientHeight > 1
}

onMounted(() => nextTick(measure))
watch(() => props.text, () => { expanded.value = false; nextTick(measure) })
watch(expanded, (open) => { if (!open) nextTick(measure) })
// Markdown images, diagrams and highlighted code can finish after mount.
// Observe the unclamped content too: the outer height stops changing at the cap.
useResizeObserver([textEl, contentEl], () => measure())
</script>

<template>
  <div>
    <!-- A height cap also bounds block content such as tables and code fences,
         which CSS line-clamp cannot reliably count as prose lines. -->
    <div
      ref="textEl"
      class="overflow-hidden break-words"
      :class="expanded ? '' : 'max-h-[11lh]'"
    >
      <div ref="contentEl">
        <slot />
      </div>
    </div>
    <button
      v-if="overflowing || expanded"
      type="button"
      :aria-expanded="expanded"
      class="mt-0.5 cursor-pointer select-none text-[0.85em] text-muted-foreground transition-colors hover:text-foreground"
      @click="expanded = !expanded"
    >
      {{ expanded ? t('chat.showLess') : t('chat.showMore') }}
    </button>
  </div>
</template>
