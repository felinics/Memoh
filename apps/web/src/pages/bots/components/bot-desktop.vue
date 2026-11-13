<template>
  <div class="space-y-8">
    <SettingsSection :title="$t('bots.desktop.title')">
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
      <SettingsRow
        v-if="info.enabled"
        :label="$t('bots.desktop.liveTitle')"
      >
        <Button
          variant="outline"
          size="sm"
          @click="previewOpen = true"
        >
          {{ $t('common.open') }}
        </Button>
      </SettingsRow>
    </SettingsSection>
    <Dialog v-model:open="previewOpen">
      <DialogPanel width="3xl">
        <DialogHeader>
          <DialogTitle>{{ $t('bots.desktop.liveTitle') }}</DialogTitle>
        </DialogHeader>
        <DialogBody>
          <div class="relative aspect-[4/3] w-full">
            <DisplayPane
              v-if="previewOpen && props.botId"
              :key="props.botId"
              :bot-id="props.botId"
              tab-id="settings-desktop"
              :title="$t('bots.desktop.liveTitle')"
              active
              :closable="false"
              class="size-full"
            />
          </div>
        </DialogBody>
      </DialogPanel>
    </Dialog>
  </div>
</template>

<script setup lang="ts">
import { onDeactivated, ref, computed, reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, Dialog, DialogPanel, DialogHeader, DialogTitle, DialogBody, SettingsRow, SettingsSection, Switch, toast } from '@felinic/ui'
import { useMutation, useQuery, useQueryCache } from '@pinia/colada'
import {
  getBotsByBotIdContainerDisplay,
  getBotsByBotIdSettings,
  putBotsByBotIdSettings,
  type HandlersDisplayInfoResponse,
  type SettingsUpsertRequest,
} from '@memohai/sdk'
import { useVisiblePoll } from '@/composables/useVisiblePoll'
import { resolveApiErrorMessage } from '@/utils/api-error'
import DisplayPane from '@/pages/home/components/display-pane.vue'

const props = defineProps<{
  botId: string
}>()

const previewOpen = ref(false)
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
  mutation: async (body: SettingsUpsertRequest) => {
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
useVisiblePoll(
  () => refetchDisplay(),
  POLL_INTERVAL_MS,
  { enabled: () => !!props.botId },
)

onDeactivated(() => {
  previewOpen.value = false
})
</script>
