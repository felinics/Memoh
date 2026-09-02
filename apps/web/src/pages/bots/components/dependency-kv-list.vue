<script setup lang="ts">
// The read-only key/value block every dependency dialog carries (target
// version, install path, digest, rollback from/to). One owner so the four
// dialogs cannot drift on padding or divider tone. Rows with an empty value
// are dropped here so callers can pass optional fields unconditionally.
import { computed } from 'vue'

export interface DependencyKvRow {
  label: string
  value?: string | null
  /** Technical values (ids, versions, paths, digests) read as mono. */
  mono?: boolean
}

const props = defineProps<{
  rows: DependencyKvRow[]
}>()

const visibleRows = computed(() => props.rows.filter(row => !!row.value?.trim()))
</script>

<template>
  <dl
    v-if="visibleRows.length"
    class="text-body"
  >
    <div
      v-for="row in visibleRows"
      :key="row.label"
      class="flex items-start justify-between gap-4 border-b border-border-soft py-2 last:border-b-0"
    >
      <dt class="shrink-0 text-muted-foreground">
        {{ row.label }}
      </dt>
      <dd
        class="min-w-0 break-all text-right text-foreground"
        :class="{ 'font-mono': row.mono }"
      >
        {{ row.value }}
      </dd>
    </div>
  </dl>
</template>
