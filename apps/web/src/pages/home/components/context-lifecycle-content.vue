<template>
  <DialogHeader>
    <DialogTitle>{{ $t('chat.lifecycle.title') }}</DialogTitle>
    <DialogDescription>
      <span
        v-for="stat in aggregateStats"
        :key="stat.key"
        class="mr-3 inline-flex items-center gap-1 tabular-nums"
      >
        {{ stat.label }}
        <span class="font-medium text-foreground">{{ stat.value }}</span>
      </span>
      <span
        v-if="aggregateStats.length"
        class="text-caption"
      >{{ $t('chat.lifecycle.aggScope') }}</span>
    </DialogDescription>
  </DialogHeader>

  <DialogBody>
    <div class="min-h-80 space-y-3">
      <div
        v-if="isLoading"
        class="space-y-1.5"
      >
        <Skeleton
          v-for="row in 5"
          :key="row"
          class="h-9 w-full"
        />
      </div>
      <Empty
        v-else-if="isError"
        class="min-h-40"
      >
        <EmptyDescription>{{ $t('chat.lifecycle.loadFailed') }}</EmptyDescription>
      </Empty>
      <template v-else>
        <p
          v-if="data?.legacy_source"
          class="text-caption text-muted-foreground"
        >
          {{ $t('chat.lifecycle.legacySource') }}
        </p>
        <ContextLifecycleTurns
          :turns="turns"
          :has-older="data?.has_more === true || data?.legacy_history_may_exist === true"
        />
        <div
          v-if="data?.has_more || data?.legacy_history_may_exist"
          class="flex items-center justify-between gap-2 text-caption text-muted-foreground"
        >
          <span>{{ $t('chat.lifecycle.moreTurns') }}</span>
          <Button
            v-if="canLoadOlder"
            variant="ghost"
            size="sm"
            @click="loadOlder"
          >
            {{ $t('chat.lifecycle.loadOlder', { n: maxLimit }) }}
          </Button>
        </div>
      </template>
    </div>
  </DialogBody>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, DialogBody, DialogDescription, DialogHeader, DialogTitle, Empty, EmptyDescription, Skeleton } from '@felinic/ui'
import { useContextLifecycle } from '../composables/useContextLifecycle'
import { formatTokenCount } from '../composables/context-categories'
import ContextLifecycleTurns from './context-lifecycle-turns.vue'

const { t } = useI18n()
const { data, status, hasTarget, canLoadOlder, loadOlder, maxLimit } = useContextLifecycle()

// A pane without a session never fetches; showing its empty state beats a
// skeleton that would spin forever.
const isLoading = computed(() => hasTarget.value && status.value === 'pending')
const isError = computed(() => status.value === 'error')
const turns = computed(() => data.value?.turns ?? [])

// Cache totals are facts observed at Memoh's boundary; an unobserved zero
// (ACP) is not a measurement, so only positive totals earn a row.
const aggregateStats = computed(() => {
  const aggregates = data.value?.aggregates
  if (!aggregates) return []
  const stats = [{ key: 'turns', label: t('chat.lifecycle.aggTurns'), value: String(aggregates.turns ?? 0) }]
  if ((aggregates.total_cache_read_tokens ?? 0) > 0) {
    stats.push({ key: 'cacheRead', label: t('chat.lifecycle.aggCacheRead'), value: formatTokenCount(aggregates.total_cache_read_tokens ?? 0) })
  }
  if ((aggregates.total_cache_write_tokens ?? 0) > 0) {
    stats.push({ key: 'cacheWrite', label: t('chat.lifecycle.aggCacheWrite'), value: formatTokenCount(aggregates.total_cache_write_tokens ?? 0) })
  }
  return stats
})
</script>
