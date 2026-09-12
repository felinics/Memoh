<template>
  <ComposerCapsule
    compact
    :label="$t('chat.goal.label')"
  >
    <ComposerPanelError
      v-if="error"
      :message="error"
    />
    <div
      v-else-if="goal"
      class="flex min-w-0 items-center gap-2 text-label"
    >
      <Target class="size-4 shrink-0 text-muted-foreground" />
      <span class="shrink-0">{{ $t(`chat.goal.status.${goal.status}`) }}</span>
      <span
        class="min-w-0 flex-1 truncate text-muted-foreground"
        :title="goal.objective"
      >{{ goal.objective }}</span>
      <span class="shrink-0 text-caption tabular-nums text-muted-foreground">{{ $t('chat.goal.elapsed', { seconds: elapsedSeconds }) }}</span>
      <span
        v-if="goal.status !== 'complete'"
        class="inline-flex"
        :title="goalActionLabel"
      >
        <Button
          variant="ghost"
          size="icon-sm"
          :disabled="disabled || (goal.status !== 'active' && resumeDisabled)"
          :aria-label="goalActionLabel"
          @click="goal.status === 'active' ? emit('pause') : emit('resume')"
        >
          <Pause v-if="goal.status === 'active'" />
          <Play v-else />
        </Button>
      </span>
      <Button
        variant="ghost"
        size="icon-sm"
        :disabled="disabled"
        :title="$t('chat.goal.clear')"
        :aria-label="$t('chat.goal.clear')"
        @click="emit('clear')"
      >
        <Trash2 />
      </Button>
    </div>
  </ComposerCapsule>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useTimestamp } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import { Pause, Play, Target, Trash2 } from 'lucide-vue-next'
import { Button } from '@felinic/ui'
import type { ExternalGoal } from '@memohai/sdk'
import ComposerCapsule from './composer-capsule.vue'
import ComposerPanelError from './composer-panel-error.vue'

const props = defineProps<{ goal?: ExternalGoal | null; error?: string; disabled: boolean; resumeDisabled: boolean; resumeDisabledReason?: string; streaming: boolean }>()
const emit = defineEmits<{ pause: []; resume: []; clear: [] }>()
const { t } = useI18n()
const goalActionLabel = computed(() => props.goal?.status === 'active' ? t('chat.goal.pause') : props.resumeDisabledReason || t('chat.goal.resume'))

const now = useTimestamp({ interval: 1000 })
const syncedAt = ref(now.value)
// Unchanged polling responses must not restart the local clock: Codex may
// publish accumulated time less often than the status endpoint is refreshed.
watch(
  [() => props.goal?.objective, () => props.goal?.status, () => props.goal?.time_used_seconds, () => props.streaming],
  () => { syncedAt.value = Date.now() },
)
const elapsedSeconds = computed(() => {
  const elapsed = props.goal?.time_used_seconds ?? 0
  if (props.goal?.status !== 'active' || !props.streaming) return elapsed
  return elapsed + Math.max(0, Math.floor((now.value - syncedAt.value) / 1000))
})
</script>
