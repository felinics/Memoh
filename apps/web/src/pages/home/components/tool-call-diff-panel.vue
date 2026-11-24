<script setup lang="ts">
import { useCodeSurfaceAppearance } from '@/components/code-surface/theme'
import PatchSurface from '@/components/code-surface/PatchSurface.vue'

// Read-only unified diff shared by the edit, write and apply_patch tool
// details. stream-diffs renders the rows: gutter line numbers (old for
// removals, new for additions/context), whole-row red/green bands, inline
// emphasis on the replaced fragments, and the `- / +` counts.
defineProps<{ diff: string }>()

const { theme, themeType, fontSize, lineHeight, fontFamily } = useCodeSurfaceAppearance()
</script>

<template>
  <PatchSurface
    :patch="diff"
    :theme="theme"
    :theme-type="themeType"
    :font-size="fontSize"
    :line-height="lineHeight"
    :font-family="fontFamily"
    max-height="24rem"
  />
</template>
