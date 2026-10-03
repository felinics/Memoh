<template>
  <!-- Same r=10 outline as Lucide's CircleUserRound / Info, so the circles that
       stack in the settings nav share one diameter and one left edge. The
       inner lines stop just short of the circle so the ends don't blob. -->
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
    <circle
      cx="12"
      cy="12"
      r="10"
    />
    <line
      x1="12"
      y1="2"
      x2="12"
      y2="22"
    />
    <line
      x1="12"
      y1="8.11"
      x2="20.22"
      y2="8.11"
    />
    <line
      x1="12"
      y1="12"
      x2="21.44"
      y2="12"
    />
    <line
      x1="12"
      y1="15.89"
      x2="20.22"
      y2="15.89"
    />
  </svg>
</template>

<script setup lang="ts">
withDefaults(defineProps<{
  strokeWidth?: number | string
}>(), {
  strokeWidth: 2,
})
</script>
