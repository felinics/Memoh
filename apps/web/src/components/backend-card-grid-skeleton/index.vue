<script setup lang="ts">
import { Skeleton } from '@felinic/ui'

// Placeholder grid for the BackendCard lists (providers / voice / video settings).
// It gates the list until the source queries resolve so the FIRST painted grid is
// already the final one. These lists put late-arriving entries at the TOP —
// enabled providers sort first, provider cards render before template drafts, and
// hosted builds inject managed backends — so a grid that reflows after paint turns
// a click aimed at one card into a click on whichever card moved under the pointer.
withDefaults(defineProps<{
  count?: number
}>(), {
  count: 4,
})
</script>

<template>
  <!-- Mirrors BackendCard geometry (p-3.5, size-10 round leading, two text lines)
       so the swap to real cards doesn't shift the layout either. -->
  <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
    <div
      v-for="i in count"
      :key="i"
      class="flex items-center gap-3 rounded-menu-shell border border-border bg-card p-3.5 dark:border-0"
    >
      <Skeleton class="size-10 shrink-0 rounded-full" />
      <div class="min-w-0 flex-1">
        <Skeleton class="h-4 w-2/5" />
        <Skeleton class="mt-1.5 h-3 w-3/5" />
      </div>
    </div>
  </div>
</template>
