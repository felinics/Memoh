<!-- Upload-style share glyph (up arrow + open tray), same shape as lucide's
     `Share` but retuned so it doesn't read as a tall thin "stick": the arrow
     head is flatter/wider, the shaft is shorter, and the tray is wider — so the
     bounding box is near-square instead of 1.25:1 tall. Drawn in lucide's stroke
     language (24px grid, round caps/joins, currentColor) so it sits next to the
     other action icons cleanly. strokeWidth is a prop to match the row. -->
<template>
  <svg
    xmlns="http://www.w3.org/2000/svg"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    :stroke-width="strokeWidth"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    <path d="M3 13v5a3 3 0 0 0 3 3h12a3 3 0 0 0 3-3v-5" />
    <path d="M7 7 12 2l5 5" />
    <path d="M12 2.5V14" />
  </svg>
</template>

<script setup lang="ts">
import { uiIconStrokeWidth } from '@memohai/icon/ui'

withDefaults(defineProps<{ strokeWidth?: number }>(), { strokeWidth: uiIconStrokeWidth })
</script>
