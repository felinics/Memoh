<template>
  <!-- Two placeholder rows: accounts on paid plans report a short and a weekly
       window, so the loaded rows land in the same frame. -->
  <template v-if="usage.isLoading.value && !usage.data.value">
    <SettingsRow
      v-for="index in 2"
      :key="index"
      stack="sm"
    >
      <template #content>
        <Skeleton class="h-4 w-24" />
        <Skeleton class="mt-1.5 h-3 w-32" />
      </template>
      <Skeleton class="h-1.5 w-full sm:w-48" />
    </SettingsRow>
  </template>
  <SettingsRow
    v-else-if="usage.error.value && !usage.data.value"
    :label="$t('bots.agent.usage.window.unknown')"
    :description="resolveApiErrorMessage(usage.error.value, $t('errors.agent_credential.usage_unavailable'))"
  />
  <template v-else>
    <SettingsRow
      v-for="window in usage.data.value?.windows ?? []"
      :key="window.window_minutes"
      :label="codexUsageWindowLabel(window.window_minutes, t)"
      :description="windowDescription(window)"
      stack="sm"
    >
      <div class="flex w-full items-center gap-3 sm:w-48">
        <Progress :model-value="Math.min(window.used_percent, 100)" />
        <span class="w-9 shrink-0 text-right text-body tabular-nums text-muted-foreground">{{ window.used_percent }}%</span>
      </div>
    </SettingsRow>
  </template>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Progress, SettingsRow, Skeleton } from '@felinic/ui'
import type { ExternalagentCodexUsageWindow } from '@memohai/sdk'
import { useCodexUsage } from '@/composables/useCodexUsage'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { codexUsageResetTime, codexUsageWindowLabel } from '@/utils/codex-usage'

const props = defineProps<{
  botId: string
  botAgentId: string
}>()
const { t, locale } = useI18n()

const usage = useCodexUsage({
  botId: computed(() => props.botId),
  botAgentId: computed(() => props.botAgentId),
  enabled: () => true,
})

function windowDescription(window: ExternalagentCodexUsageWindow): string {
  const exhausted = window.used_percent >= 100
  if (!window.resets_at) return exhausted ? t('bots.agent.usage.exhaustedNoReset') : ''
  const time = codexUsageResetTime(window.resets_at, locale.value)
  return exhausted ? t('bots.agent.usage.exhausted', { time }) : t('bots.agent.usage.resets', { time })
}
</script>
