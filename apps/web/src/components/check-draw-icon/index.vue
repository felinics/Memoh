<!-- Checkmark that draws itself on mount (copy confirmation). Same stroke-draw
     technique used by the provider connection test: pathLength="1" normalizes the
     path so the dash math is length-agnostic — the glyph starts fully hidden
     (offset 1) and is drawn on (offset 0), so it appears by being drawn rather
     than popping in. Mounting the element (v-if on copy) re-triggers the draw.
     strokeWidth is a prop so it matches the row it sits in. -->
<template>
  <svg
    class="check-draw"
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
      d="M4 12.5 9 17.5 20 6.5"
      pathLength="1"
    />
  </svg>
</template>

<script setup lang="ts">
withDefaults(defineProps<{ strokeWidth?: number }>(), { strokeWidth: 2 })
</script>

<style scoped>
.check-draw path {
  stroke-dasharray: 1;
  stroke-dashoffset: 1;
  animation: check-draw 0.3s cubic-bezier(0.65, 0, 0.35, 1) forwards;
}

@keyframes check-draw {
  to {
    stroke-dashoffset: 0;
  }
}

@media (prefers-reduced-motion: reduce) {
  .check-draw path {
    animation: none;
    stroke-dashoffset: 0;
  }
}
</style>
