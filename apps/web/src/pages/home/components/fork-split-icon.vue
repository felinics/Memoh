<!-- Local variant of lucide's Split icon. It starts from the stock Split paths,
     fixes the 90deg fork orientation in the SVG itself, and only lengthens the
     shared stem slightly so the action still reads like the lucide glyph. -->
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
    <g transform="rotate(90 12 12)">
      <path d="M16 3h5v5" />
      <path d="M8 3H3v5" />
      <path d="M12 23.5v-9.8a4 4 0 0 0-1.172-2.872L3 3" />
      <path d="m15 9 6-6" />
    </g>
  </svg>
</template>

<script setup lang="ts">
import { uiIconStrokeWidth } from '@memohai/icon/ui'

withDefaults(defineProps<{ strokeWidth?: number }>(), { strokeWidth: uiIconStrokeWidth })
</script>
