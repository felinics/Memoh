<template>
  <PageShell
    variant="tab"
    :title="$t('bots.desktop.title')"
  >
    <div class="space-y-8">
      <!-- No section title: the page heading already says "Desktop", so a section
           titled "Desktop" over a row labelled "Desktop" would stack the same word
           three deep. The toggle's own label carries it. -->
      <SettingsSection>
        <SettingsRow
          :label="$t('bots.settings.desktopEnabled')"
          :description="$t('bots.settings.desktopEnabledDescription')"
        >
          <Switch
            :model-value="settingsForm.display_enabled"
            :disabled="isSaving"
            @update:model-value="(val) => handleToggleDisplay(!!val)"
          />
        </SettingsRow>
      </SettingsSection>

      <!-- The live screen is the whole point of the page: when Desktop is on, this
           is what the 99% came to see. The view speaks for itself — connecting,
           installing, live, or "can't reach it" all render inside DisplayPane — so
           there is no separate readiness grid restating it in flags. -->
      <section
        v-if="info.enabled"
        class="space-y-2.5"
      >
        <h2 class="px-2 text-[13px] font-medium text-muted-foreground">
          {{ $t('bots.desktop.liveTitle') }}
        </h2>
        <div class="relative aspect-[4/3] w-full overflow-hidden rounded-[var(--radius-menu-shell)] border border-border bg-card">
          <DisplayPane
            v-if="props.botId"
            :key="props.botId"
            :bot-id="props.botId"
            tab-id="settings-desktop"
            :title="$t('bots.desktop.liveTitle')"
            active
            :closable="false"
            class="size-full"
          />
        </div>
      </section>
    </div>
  </PageShell>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { PageShell, SettingsRow, SettingsSection, Switch, toast } from '@felinic/ui'
import { useMutation, useQuery, useQueryCache } from '@pinia/colada'
import {
  getBotsByBotIdContainerDisplay,
  getBotsByBotIdSettings,
  putBotsByBotIdSettings,
  type HandlersDisplayInfoResponse,
  type SettingsSettings,
} from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'
import DisplayPane from '@/pages/home/components/display-pane.vue'

const props = defineProps<{
  botId: string
}>()

const { t } = useI18n()
const queryCache = useQueryCache()

const settingsForm = reactive({
  display_enabled: false,
})

const { data: settings } = useQuery({
  key: () => ['bot-settings', props.botId],
  query: async () => {
    const { data } = await getBotsByBotIdSettings({
      path: { bot_id: props.botId },
      throwOnError: true,
    })
    return data
  },
  enabled: () => !!props.botId,
})

const { data: displayInfo, refetch: refetchDisplay } = useQuery({
  key: () => ['bot-display-info', props.botId],
  query: async () => {
    const { data } = await getBotsByBotIdContainerDisplay({
      path: { bot_id: props.botId },
      throwOnError: true,
    })
    return data
  },
  enabled: () => !!props.botId,
  refetchOnWindowFocus: true,
})

const { mutateAsync: updateSettings, isLoading: isSaving } = useMutation({
  mutation: async (body: Partial<SettingsSettings>) => {
    const { data } = await putBotsByBotIdSettings({
      path: { bot_id: props.botId },
      body,
      throwOnError: true,
    })
    return data
  },
  onSettled: () => queryCache.invalidateQueries({ key: ['bot-settings', props.botId] }),
})

watch(settings, (value) => {
  settingsForm.display_enabled = value?.display_enabled ?? false
}, { immediate: true })

async function handleToggleDisplay(enabled: boolean) {
  const previous = settingsForm.display_enabled
  settingsForm.display_enabled = enabled
  try {
    await updateSettings({ display_enabled: enabled })
    await refetchDisplay()
    toast.success(enabled ? t('bots.desktop.desktopEnabledSuccess') : t('bots.desktop.desktopDisabledSuccess'))
  } catch (error) {
    settingsForm.display_enabled = previous
    toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
  }
}

const info = computed<HandlersDisplayInfoResponse>(() => displayInfo.value ?? {})

// Silent freshness instead of a manual Refresh button: the live screen streams on
// its own, so we only quietly re-read status while the tab is actually on screen.
const POLL_INTERVAL_MS = 10_000
let pollTimer: ReturnType<typeof setInterval> | null = null

function pageVisible() {
  return typeof document === 'undefined' || document.visibilityState === 'visible'
}

function pollNow() {
  if (!pageVisible() || !props.botId) return
  void refetchDisplay()
}

function startPoll() {
  stopPoll()
  pollTimer = setInterval(pollNow, POLL_INTERVAL_MS)
}

function stopPoll() {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

function handleVisibilityChange() {
  if (pageVisible()) {
    pollNow()
    startPoll()
  } else {
    stopPoll()
  }
}

onMounted(() => {
  document.addEventListener('visibilitychange', handleVisibilityChange)
  startPoll()
})

onBeforeUnmount(() => {
  document.removeEventListener('visibilitychange', handleVisibilityChange)
  stopPoll()
})
</script>
