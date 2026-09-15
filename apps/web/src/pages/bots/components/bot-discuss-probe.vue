<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { SectionGroup, SettingsRow, SettingsSection, toast } from '@felinic/ui'
import ModelSelect from './model-select.vue'
import { filterDiscussProbeModels } from './discuss-probe-models'
import { getBotsByBotIdSettings, getModels, getProviders, putBotsByBotIdSettings } from '@memohai/sdk'
import type { SettingsSettings, SettingsUpsertRequest } from '@memohai/sdk'
import { useQuery, useQueryCache } from '@pinia/colada'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { useAutosaveQueue, type AutosaveJob } from '@/composables/use-autosave-queue'
import type { Ref } from 'vue'

const props = defineProps<{
  botId: string
}>()

const { t } = useI18n()
const botIdRef = computed(() => props.botId) as Ref<string>
const queryCache = useQueryCache()

const { data: settings } = useQuery({
  key: () => ['bot-settings', botIdRef.value],
  query: async () => {
    const { data } = await getBotsByBotIdSettings({ path: { bot_id: botIdRef.value }, throwOnError: true })
    return data
  },
  enabled: () => !!botIdRef.value,
})

const { data: modelData } = useQuery({
  key: ['models'],
  query: async () => {
    const { data } = await getModels({ throwOnError: true })
    return data
  },
})

const { data: providerData } = useQuery({
  key: ['providers'],
  query: async () => {
    const { data } = await getProviders({ throwOnError: true })
    return data
  },
})

const probeModels = computed(() => filterDiscussProbeModels(modelData.value ?? [], providerData.value ?? []))
const providers = computed(() => providerData.value ?? [])

// Autosaved, no Save button — same contract as bot-compaction.vue.
type ProbeForm = {
  discuss_probe_model_id: string
}

const form = reactive<ProbeForm>({ discuss_probe_model_id: '' })
// Last-known-server snapshot; any non-user write to `form` must advance it in
// the same block or the diff misreads it as an edit.
const synced = reactive<ProbeForm>({ ...form })

watch(settings, (val: SettingsSettings | undefined) => {
  if (!val) return
  const next = val.discuss_probe_model_id ?? ''
  // Per-field guard: a refetch landing mid-edit must not clobber it.
  if (form.discuss_probe_model_id === synced.discuss_probe_model_id) {
    form.discuss_probe_model_id = next
  }
  synced.discuss_probe_model_id = next
}, { immediate: true })

function buildJobs(changed: (keyof ProbeForm)[]): AutosaveJob<ProbeForm>[] {
  const payload: SettingsUpsertRequest = {}
  const sent: Partial<ProbeForm> = {}
  for (const key of changed) {
    sent[key] = form[key]
    // Sent explicitly even when empty: "" clears the override back to the
    // title-model fallback, while omitting the key would mean "keep current".
    ;(payload as Record<string, unknown>)[key] = form[key]
  }
  return [{
    payload: sent,
    save: async () => {
      await putBotsByBotIdSettings({
        path: { bot_id: botIdRef.value },
        body: payload,
        throwOnError: true,
      })
    },
    onError: error => toast.error(resolveApiErrorMessage(error, t('common.saveFailed'))),
  }]
}

useAutosaveQueue<ProbeForm>({
  form,
  synced,
  buildJobs,
  onDrained: () => queryCache.invalidateQueries({ key: ['bot-settings', botIdRef.value] }),
})
</script>

<template>
  <SectionGroup
    :title="$t('bots.settings.discussProbeGate')"
    :description="$t('bots.settings.discussProbeGateDescription')"
  >
    <SettingsSection>
      <SettingsRow
        :label="$t('bots.settings.discussProbeModel')"
        :description="$t('bots.settings.discussProbeModelDescription')"
      >
        <ModelSelect
          v-model="form.discuss_probe_model_id"
          :models="probeModels"
          :providers="providers"
          model-type="chat"
          :placeholder="$t('bots.settings.discussProbeModelPlaceholder')"
          :none-label="$t('bots.settings.discussProbeModelPlaceholder')"
        />
      </SettingsRow>
    </SettingsSection>
  </SectionGroup>
</template>
