<script setup lang="ts">
import { computed, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, Skeleton } from '@felinic/ui'
import { ChevronLeft } from 'lucide-vue-next'
import type { HandlersContextTrajectoryBlock, HandlersContextTrajectoryEntry } from '@memohai/sdk'
import { isApiErrorCode } from '@/utils/api-error'
import { useContextTrajectoryEvent } from '../../composables/useContextTrajectoryEvent'
import { captureStageLabel } from '../../composables/context-trajectory-labels'
import type { ContextBlockComparison } from '../../composables/context-trajectory.types'
import TrajectoryCaptureBlock from './trajectory-capture-block.vue'

const props = defineProps<{ event: HandlersContextTrajectoryEntry, previousEventId?: string | null, canLoadOlder?: boolean }>()
const emit = defineEmits<{ selectEvent: [id: string], loadOlder: [] }>()
const { t, te } = useI18n()
const comparing = shallowRef(false)
const current = useContextTrajectoryEvent(computed(() => props.event.id))
const previous = useContextTrajectoryEvent(computed(() => comparing.value ? props.previousEventId : null))
const forbidden = shallowRef(false)
const unavailable = shallowRef(false)
watch([current.error, previous.error], (errors) => {
  if (errors.some(error => isApiErrorCode(error, 'context_lifecycle.access_denied'))) forbidden.value = true
  if (errors.some(error => isApiErrorCode(error, 'context_lifecycle.not_found') || isApiErrorCode(error, 'context_lifecycle.authentication_required'))) unavailable.value = true
}, { immediate: true })
const comparisonReady = computed(() => comparing.value && previous.status.value === 'success' && !!previous.data.value)
const title = computed(() => captureStageLabel(props.event.stage, t, te))
watch(() => props.event.id, () => { comparing.value = false; forbidden.value = false; unavailable.value = false }, { flush: 'sync' })

async function refreshCurrent() {
  await current.refresh()
  if (current.status.value === 'success') { forbidden.value = false; unavailable.value = false }
}

function keyed(blocks: readonly HandlersContextTrajectoryBlock[]) {
  const occurrences = new Map<string, number>()
  return blocks.map((block) => {
    const identity = JSON.stringify([block.kind, block.label])
    const occurrence = occurrences.get(identity) ?? 0
    occurrences.set(identity, occurrence + 1)
    return { key: `${identity}/${occurrence}`, block }
  })
}

const blockRows = computed<ContextBlockComparison[]>(() => {
  const before = new Map(keyed(comparisonReady.value ? previous.data.value?.blocks ?? [] : []).map(item => [item.key, item.block]))
  const rows: ContextBlockComparison[] = keyed(current.data.value?.blocks ?? []).map(({ key, block }) => {
    const prior = before.get(key)
    before.delete(key)
    return { key, before: prior, after: block, change: !prior ? 'added' : !prior.available || !block.available ? 'unavailable' : prior.hash && prior.hash === block.hash ? 'unchanged' : 'changed' }
  })
  for (const [key, block] of before) rows.push({ key, before: block, change: 'removed' })
  return rows
})
</script>

<template>
  <div
    class="min-w-0 space-y-3"
    data-testid="trajectory-capture-inspector"
  >
    <h3 class="text-label font-semibold">
      {{ title }}
    </h3>
    <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-caption">
      <dt class="text-muted-foreground">
        {{ $t('chat.trajectory.captureRunLabel') }}
      </dt>
      <dd class="break-all font-mono">
        {{ event.run_id }}
      </dd>
      <dt class="text-muted-foreground">
        {{ $t('chat.trajectory.captureSegment') }}
      </dt>
      <dd class="break-all font-mono">
        {{ event.capture_id }}
      </dd>
      <dt class="text-muted-foreground">
        {{ $t('chat.trajectory.captureSequence') }}
      </dt>
      <dd class="tabular-nums">
        {{ event.sequence }}
      </dd>
      <dt class="text-muted-foreground">
        {{ $t('chat.trajectory.captureRecordedAt') }}
      </dt>
      <dd class="break-all font-mono">
        {{ event.recorded_at }}
      </dd>
      <template v-if="event.request">
        <dt class="text-muted-foreground">
          {{ $t('chat.trajectory.captureRequestSequence') }}
        </dt>
        <dd class="tabular-nums">
          {{ event.request }}
        </dd>
      </template>
    </dl>
    <p
      v-if="event.stage === 'external_handoff'"
      class="text-caption text-muted-foreground"
    >
      {{ $t('chat.trajectory.captureExternalBoundary') }}
    </p>
    <p
      v-if="(event.capture_errors ?? 0) > 0"
      class="text-caption text-warning"
    >
      {{ $t('chat.trajectory.captureGaps') }}
    </p>
    <div class="flex flex-wrap items-center gap-1">
      <template v-if="previousEventId">
        <Button
          variant="ghost"
          size="sm"
          @click="emit('selectEvent', previousEventId)"
        >
          <ChevronLeft />{{ $t('chat.trajectory.capturePrevious') }}
        </Button>
        <Button
          variant="ghost"
          size="sm"
          :aria-pressed="comparing"
          data-testid="capture-compare"
          @click="comparing = !comparing"
        >
          {{ $t(comparing ? 'chat.trajectory.captureCloseComparison' : 'chat.trajectory.captureCompare') }}
        </Button>
      </template>
      <Button
        v-else-if="canLoadOlder && previousEventId === undefined && (event.sequence ?? 0) > 1"
        variant="ghost"
        size="sm"
        @click="emit('loadOlder')"
      >
        {{ $t('chat.trajectory.captureLoadPrevious') }}
      </Button>
    </div>
    <div
      v-if="forbidden"
      class="space-y-1"
      data-testid="capture-forbidden"
    >
      <p class="text-caption text-muted-foreground">
        {{ $t('chat.trajectory.inspectorTextsForbidden') }}
      </p>
      <Button
        variant="ghost"
        size="sm"
        @click="refreshCurrent"
      >
        {{ $t('common.retry') }}
      </Button>
    </div>
    <div
      v-else-if="unavailable"
      class="space-y-1"
      role="status"
    >
      <p class="text-caption text-muted-foreground">
        {{ $t('chat.trajectory.captureNoLongerAvailable') }}
      </p>
      <Button
        variant="ghost"
        size="sm"
        @click="refreshCurrent"
      >
        {{ $t('common.retry') }}
      </Button>
    </div>
    <template v-else>
      <div
        v-if="current.status.value === 'pending'"
        class="space-y-2"
      >
        <Skeleton
          v-for="line in 4"
          :key="line"
          class="h-4 w-full"
        />
      </div>
      <div
        v-else-if="current.status.value === 'error'"
        class="space-y-1"
        role="status"
      >
        <p class="text-caption text-destructive">
          {{ $t('chat.trajectory.captureLoadFailed') }}
        </p>
        <Button
          variant="ghost"
          size="sm"
          @click="current.refresh()"
        >
          {{ $t('common.retry') }}
        </Button>
      </div>
      <template v-if="current.data.value">
        <p
          v-if="!current.data.value.complete"
          class="text-caption text-warning"
          data-testid="capture-incomplete"
        >
          {{ $t('chat.trajectory.captureIncomplete') }}
        </p>
        <p
          v-if="comparisonReady && previous.data.value?.complete === false"
          class="text-caption text-warning"
        >
          {{ $t('chat.trajectory.capturePreviousIncomplete') }}
        </p>
        <p
          v-if="comparing && previous.status.value === 'pending'"
          class="text-caption text-muted-foreground"
        >
          {{ $t('chat.trajectory.captureLoadingPrevious') }}
        </p>
        <div
          v-if="comparing && previous.status.value === 'error'"
          class="space-y-1"
          role="status"
        >
          <p class="text-caption text-warning">
            {{ $t('chat.trajectory.capturePreviousFailed') }}
          </p>
          <Button
            variant="ghost"
            size="sm"
            @click="previous.refresh()"
          >
            {{ $t('common.retry') }}
          </Button>
        </div>
        <section
          v-for="row in blockRows"
          :key="row.key"
          class="min-w-0 space-y-1 border-t border-border pt-2"
        >
          <div class="flex items-start justify-between gap-2">
            <h4 class="min-w-0 break-all font-mono text-body font-medium">
              {{ row.after?.label || row.before?.label || row.after?.kind || row.before?.kind }}
            </h4>
            <span
              v-if="comparisonReady"
              class="shrink-0 text-caption text-muted-foreground"
            >{{ $t(`chat.trajectory.captureChange.${row.change}`) }}</span>
          </div>
          <TrajectoryCaptureBlock
            v-if="comparisonReady && row.before && row.change !== 'unchanged'"
            :key="`${event.id}/${row.key}/before`"
            :block="row.before"
            :label="$t('chat.trajectory.captureBefore')"
          />
          <TrajectoryCaptureBlock
            v-if="row.after"
            :key="`${event.id}/${row.key}/after`"
            :block="row.after"
            :label="$t('chat.trajectory.captureCurrent')"
          />
        </section>
      </template>
    </template>
  </div>
</template>
