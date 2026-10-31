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
useResizeObserver(textEl, () => measure())
</script>

<template>
  <div>
    <!-- Collapsed clamp is a literal class so Tailwind's JIT actually emits it
         (line-clamp-[11] = 11 visible lines; the toggle takes the 12th). The
         text is set as one plain run in .chat-user-text — the rule the in-place
         edit field shares, so entering edit changes no glyph (see style.css). -->
    <!-- eslint-disable vue/multiline-html-element-content-newline -- no breaks
         around the text: under white-space: pre-wrap a source newline would
         render as a literal blank line in the prompt. -->
    <div
      ref="textEl"
      class="chat-user-text whitespace-pre-wrap break-words"
      :class="expanded ? '' : 'line-clamp-[11]'"
    >{{ text }}</div>
    <!-- eslint-enable vue/multiline-html-element-content-newline -->
    <button
      v-if="overflowing || expanded"
      type="button"
      class="mt-0.5 cursor-pointer select-none text-[0.85em] text-muted-foreground transition-colors hover:text-foreground"
      @click="expanded = !expanded"
    >
      {{ expanded ? t('chat.showLess') : t('chat.showMore') }}
    </button>
  </div>
</template>
