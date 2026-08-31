<template>
  <Dialog v-model:open="open">
    <DialogPanel>
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
        </DialogDescription>
      </DialogHeader>

      <DialogBody>
        <div class="min-h-80">
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
          <p
            v-else-if="isError"
            class="py-10 text-center text-body text-muted-foreground"
          >
            {{ $t('chat.lifecycle.loadFailed') }}
          </p>
          <ContextLifecycleTurns
            v-else
            :turns="turns"
          />
        </div>
      </DialogBody>
    </DialogPanel>
  </Dialog>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Dialog, DialogBody, DialogDescription, DialogHeader, DialogPanel, DialogTitle, Skeleton } from '@felinic/ui'
import { useContextLifecycle } from '../composables/useContextLifecycle'
import { formatTokenCount } from '../composables/context-categories'
import ContextLifecycleTurns from './context-lifecycle-turns.vue'

const open = defineModel<boolean>('open', { default: false })

const { t } = useI18n()
const { data, status } = useContextLifecycle({ open })

const isLoading = computed(() => status.value === 'pending')
const isError = computed(() => status.value === 'error')
const turns = computed(() => data.value?.turns ?? [])

const aggregateStats = computed(() => {
  const aggregates = data.value?.aggregates
  if (!aggregates) return []
  return [
    { key: 'turns', label: t('chat.lifecycle.aggTurns'), value: String(aggregates.turns ?? 0) },
    { key: 'cacheRead', label: t('chat.lifecycle.aggCacheRead'), value: formatTokenCount(aggregates.total_cache_read_tokens ?? 0) },
    { key: 'cacheWrite', label: t('chat.lifecycle.aggCacheWrite'), value: formatTokenCount(aggregates.total_cache_write_tokens ?? 0) },
  ]
})
</script>
