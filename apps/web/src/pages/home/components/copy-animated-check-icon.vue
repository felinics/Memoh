<!-- Copy confirmation check, ported from Arkloop
     src/apps/web/src/components/AnimatedCheck.tsx (c9ca6979e). Kept separate from
     the shared check-draw-icon on purpose: the copy button's timing is tuned
     around this exact draw. The path is lucide's check (length ≈ 21.3) but the
     dash is 30, so the first ~30% of the 150ms draw shows nothing; that hidden
     stretch overlaps the copy button's invisible "entering" phase, and the stroke
     finishes as the glyph finishes growing back. Do not normalize it with
     pathLength — that removes the delay the copy timing relies on. -->
<template>
  <svg
    class="copy-animated-check"
    xmlns="http://www.w3.org/2000/svg"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    :stroke-width="strokeWidth"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    <path
      d="M4 13l4 4L20 7"
      stroke-dasharray="30"
      stroke-dashoffset="30"
    />
  </svg>
</template>

<script setup lang="ts">
withDefaults(defineProps<{ strokeWidth?: number }>(), { strokeWidth: 2 })
</script>

<style scoped>
.copy-animated-check path {
  animation: copy-draw-check 0.15s ease-out forwards;
}

@keyframes copy-draw-check {
  from {
    stroke-dashoffset: 30;
  }
  to {
    stroke-dashoffset: 0;
  }
}

@media (prefers-reduced-motion: reduce) {
  .copy-animated-check path {
    animation: none;
    stroke-dashoffset: 0;
  }
}
</style>
