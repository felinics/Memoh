<template>
  <!-- Memory "Advanced": one ActionCard row on the Memories tab opening a
       dialog. Memory works out of the box on the bot's chat model, so the
       knobs a new user never needs to understand live here instead of on a
       settings page: the memory model override, the team's embedding model,
       and manual index sync. Each row follows the owner rhythm — label +
       description on the left, the control on the right. The memory model
       commits on change; the embedding model waits for an explicit Save
       because each save re-instantiates the team's memory provider. Index
       counts stay on Overview. -->
  <section class="mb-6">
    <ActionCard
      :title="$t('bots.memory.advanced.entryTitle')"
      @click="dialogOpen = true"
    >
      <template #icon>
        <SlidersHorizontal />
      </template>
    </ActionCard>

    <Dialog v-model:open="dialogOpen">
      <DialogPanel>
        <DialogHeader>
          <DialogTitle>{{ $t('bots.memory.advanced.entryTitle') }}</DialogTitle>
        </DialogHeader>
        <DialogBody>
          <div class="flex flex-col gap-4">
            <div class="flex min-h-[2.25rem] items-center justify-between gap-4">
              <div class="min-w-0">
                <p class="text-sm font-medium text-foreground">
                  {{ $t('bots.memory.advanced.memoryModel') }}
                </p>
                <p class="mt-0.5 text-xs text-muted-foreground">
                  {{ $t('bots.memory.advanced.memoryModelDescription') }}
                </p>
              </div>
              <div class="w-52 shrink-0">
                <ModelSelect
                  v-model="memoryModelId"
                  :models="models"
                  :providers="providers"
                  popover-align="end"
                  model-type="chat"
                  :placeholder="$t('bots.memory.advanced.memoryModelPlaceholder')"
                  :none-label="$t('bots.memory.advanced.memoryModelPlaceholder')"
                />
              </div>
            </div>

            <div class="flex min-h-[2.25rem] items-center justify-between gap-4">
              <div class="min-w-0">
                <p class="text-sm font-medium text-foreground">
                  {{ $t('memory.semanticEmbeddingModel') }}
                </p>
                <p class="mt-0.5 text-xs text-muted-foreground">
                  {{ $t('bots.memory.advanced.embeddingModelDescription') }}
                </p>
              </div>
              <div class="flex shrink-0 items-center gap-2">
                <Button
                  v-if="embeddingDirty"
                  size="sm"
                  :loading="embeddingSaving"
                  @click="saveEmbeddingModel"
                >
                  {{ $t('common.save') }}
                </Button>
                <div class="w-52">
                  <ModelSelect
                    v-model="embeddingModelId"
                    :models="models"
                    :providers="providers"
                    popover-align="end"
                    model-type="embedding"
                    :placeholder="$t('memory.semanticEmbeddingModelPlaceholder')"
                  />
                </div>
              </div>
            </div>

            <div class="flex min-h-[2.25rem] items-center justify-between gap-4">
              <div class="min-w-0">
                <p class="text-sm font-medium text-foreground">
                  {{ $t('memory.builtinTitle') }}
                </p>
                <p class="mt-0.5 text-xs text-muted-foreground">
                  {{ syncDescription }}
                </p>
              </div>
              <Button
                variant="outline"
                size="sm"
                class="shrink-0"
                :disabled="!memoryStatus?.can_manual_sync"
                :loading="syncLoading"
                @click="handleSync"
              >
                {{ $t('bots.memory.advanced.syncAction') }}
              </Button>
            </div>
          </div>
        </DialogBody>
      </DialogPanel>
    </Dialog>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { SlidersHorizontal } from 'lucide-vue-next'
import {
  ActionCard,
  Button,
  Dialog,
  DialogBody,
  DialogHeader,
  DialogPanel,
  DialogTitle,
  toast,
} from '@felinic/ui'
import { useQuery, useQueryCache } from '@pinia/colada'
import {
  getBotsByBotIdSettings,
  getMemoryConfig,
  getModels,
  getProviders,
  postBotsByBotIdMemoryRebuild,
  putBotsByBotIdSettings,
  putMemoryConfig,
} from '@memohai/sdk'
import type { AdaptersMemoryStatusResponse } from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { useServerSyncedScalar } from '@/composables/use-server-synced-form'
import ModelSelect from './model-select.vue'

const props = defineProps<{
  botId: string
  memoryStatus: AdaptersMemoryStatusResponse | null
  statusLoading?: boolean
}>()

const emit = defineEmits<{
  synced: []
}>()

const { t } = useI18n()
const dialogOpen = ref(false)
const syncLoading = ref(false)
const queryCache = useQueryCache()

// The dialog's queries only run while it is open; the keys are shared with
// the bot settings page and the model pickers so their caches stay one.
const { data: settingsData } = useQuery({
  key: () => ['bot-settings', props.botId],
  query: async () => {
    const { data } = await getBotsByBotIdSettings({ path: { bot_id: props.botId }, throwOnError: true })
    return data
  },
  enabled: () => dialogOpen.value && !!props.botId,
})

const { data: memoryConfigData } = useQuery({
  key: () => ['memory-config'],
  query: async () => {
    const { data } = await getMemoryConfig({ throwOnError: true })
    return data
  },
  enabled: () => dialogOpen.value,
})

const { data: modelData } = useQuery({
  key: () => ['models'],
  query: async () => {
    const { data } = await getModels({ throwOnError: true })
    return data
  },
  enabled: () => dialogOpen.value,
})

const { data: providerData } = useQuery({
  key: () => ['providers'],
  query: async () => {
    const { data } = await getProviders({ throwOnError: true })
    return data
  },
  enabled: () => dialogOpen.value,
})

const models = computed(() => modelData.value ?? [])
const providers = computed(() => providerData.value ?? [])

// Empty means "follow the chat model" — the server resolves it, so the
// picker never has to mirror the chat model's value.
const memoryModelId = ref('')
const embeddingModelId = ref('')

useServerSyncedScalar(memoryModelId, {
  source: () => settingsData.value,
  identity: settings => (settings === undefined ? 'loading' : props.botId),
  server: settings => settings?.memory_llm_model_id ?? '',
})

// The embedding model belongs to the team's built-in memory, not this bot;
// the row's description says so.
useServerSyncedScalar(embeddingModelId, {
  source: () => memoryConfigData.value,
  identity: config => (config === undefined ? 'loading' : 'builtin'),
  server: config => config?.embedding_model_id ?? '',
})

// Picks commit one at a time in pick order, so the server ends on the last
// pick even if the user changes it again mid-request. A failed save restores
// the last value the server confirmed — not the pick before it, which a
// newer successful save may already have replaced.
let memoryModelSaves = Promise.resolve()
watch(memoryModelId, (value) => {
  memoryModelSaves = memoryModelSaves.then(() => saveMemoryModel(value))
})

async function saveMemoryModel(value: string) {
  const confirmed = settingsData.value?.memory_llm_model_id ?? ''
  if (settingsData.value === undefined || value === confirmed) return
  try {
    const { data } = await putBotsByBotIdSettings({
      path: { bot_id: props.botId },
      body: { memory_llm_model_id: value },
      throwOnError: true,
    })
    queryCache.setQueryData(['bot-settings', props.botId], data)
    toast.success(t('memory.saveSuccess'))
  } catch (error) {
    // Only roll back when no newer pick is queued behind this one.
    if (memoryModelId.value === value) memoryModelId.value = confirmed
    toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
  }
}

const embeddingSaving = ref(false)
const embeddingDirty = computed(() =>
  memoryConfigData.value !== undefined
  && embeddingModelId.value !== (memoryConfigData.value.embedding_model_id ?? ''),
)

async function saveEmbeddingModel() {
  embeddingSaving.value = true
  try {
    const { data } = await putMemoryConfig({
      body: { embedding_model_id: embeddingModelId.value },
      throwOnError: true,
    })
    queryCache.setQueryData(['memory-config'], data)
    toast.success(t('memory.saveSuccess'))
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
  } finally {
    embeddingSaving.value = false
  }
}

const syncDescription = computed(() => {
  if (props.statusLoading) return t('common.loading')
  if (!props.memoryStatus) return t('bots.memory.advanced.healthUnavailable')
  if (props.memoryStatus.degraded) return t('bots.memory.degradedDesc')
  return t('bots.memory.advanced.healthOk')
})

async function handleSync() {
  const botId = props.botId.trim()
  if (!botId) return

  syncLoading.value = true
  try {
    const { data } = await postBotsByBotIdMemoryRebuild({
      path: { bot_id: botId },
      throwOnError: true,
    })
    toast.success(t('bots.memory.advanced.syncSuccess', {
      fsCount: data?.fs_count ?? 0,
      restoredCount: data?.restored_count ?? 0,
      storageCount: data?.storage_count ?? 0,
    }))
    emit('synced')
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.memory.advanced.syncFailed')))
  } finally {
    syncLoading.value = false
  }
}
</script>
