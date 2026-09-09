<template>
  <!--
    Owner for the collapsible header row shared by tool-call-cluster,
    tool-call-group's process header, and tool-call-inline's expandable row.
    Each had drifted independently: py-0.5 vs py-px, an unnamed `group` vs a
    named `group/h`, duration-75 present on two of three but missing on the
    cluster row. This owns the row layout AND the hover affordance (unified
    to the majority: py-px, duration-75, unnamed `group`), so the hover chrome
    lives in the component instead of being injected onto it from a caller.
    Each caller keeps its own open/toggle state and slot content, and only
    picks its neutral rest-ink `tone`. Tool execution failures are recoverable
    steps in an Agent turn, so process headers do not offer an error tone;
    diagnostics belong in the expanded tool detail.

    `nested` picks the root tag: tool-call-inline's expandable row contains a
    real nested <button> (the "open in files" action), so ITS root can't also
    be a <button> — a button-in-button is invalid HTML and breaks click
    targeting. cluster/group have no nested interactive element and use a
    native <button>, which gets Enter/Space-activates-click for free; the
    div[role=button] variant has to translate keydown itself.
  -->
  <component
    :is="nested ? 'div' : 'button'"
    v-bind="nested ? { role: 'button', tabindex: 0 } : {}"
    :aria-expanded="open"
    class="tool-header-row group flex items-center gap-1.5 w-full text-left transition-colors duration-75 cursor-pointer py-px select-none"
    :class="toneClass"
    v-on="nested ? { keydown: onKeydown } : {}"
    @click="emit('toggle')"
  >
    <slot />
  </component>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(defineProps<{
  open?: boolean
  nested?: boolean
  tone?: 'muted' | 'cop'
}>(), {
  open: false,
  nested: false,
  tone: 'cop',
})
const emit = defineEmits<{ toggle: [] }>()

// Hover is the component's own chrome, not a page injection — enumerated in
// full per tone (never string-concatenated) so Tailwind's literal-text scan
// picks up every combination.
const toneClass = computed(() => {
  if (props.tone === 'muted') return 'text-muted-foreground hover:text-foreground' /* ui-allow-style */
  return 'text-cop-title hover:text-foreground' /* ui-allow-style */
})

function onKeydown(event: KeyboardEvent) {
  // Keyboard events target the focused element. If that isn't the row itself,
  // focus sits on a nested interactive child (e.g. inline's "open in files"
  // button) — let the child's native activation run instead of also toggling
  // the row. This is the keyboard half of the button-in-button problem; the
  // mouse half is the child's own @click.stop.
  if (event.target !== event.currentTarget) return
  if (event.key !== 'Enter' && event.key !== ' ') return
  event.preventDefault()
  emit('toggle')
}
</script>

<style scoped>
/* Shimmer uses transparent text over a gradient, so changing inherited color
   alone cannot highlight it. Hover belongs to the whole interactive row. */
.tool-header-row:hover :deep(.tool-shimmer-text),
.tool-header-row:focus-visible :deep(.tool-shimmer-text) {
  background-image: none;
  color: inherit;
  -webkit-text-fill-color: currentColor;
  animation: none;
}
</style>
