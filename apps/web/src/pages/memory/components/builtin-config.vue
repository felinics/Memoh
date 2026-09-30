<template>
  <!-- Built-in Memory: one settings card holding the team's embedding-model
       row. The mode is always graph and semantic readiness is fully derived
       from whether this row's model is set; whether a bot uses memory at all
       is that bot's own switch in its settings. -->
  <SectionGroup :title="$t('memory.builtinTitle')">
    <SettingsSection>
      <SettingsRow
        :label="$t('memory.semanticEmbeddingModel')"
        :description="$t('memory.semanticIndexDescription')"
        stack="sm"
        align="start"
      >
        <div class="w-full sm:w-64">
          <ModelSelect
            v-model="embeddingModelId"
            :models="models"
            :providers="providers"
            model-type="embedding"
            :placeholder="$t('memory.semanticEmbeddingModelPlaceholder')"
          />
        </div>
      </SettingsRow>
    </SettingsSection>
  </SectionGroup>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useServerSyncedScalar } from '@/composables/use-server-synced-form'
import { SectionGroup, SettingsRow, SettingsSection, toast } from '@felinic/ui'
import { useQuery, useQueryCache } from '@pinia/colada'
import {
  getMemoryConfig,
  getModels,
  getProviders,
  putMemoryConfig,
} from '@memohai/sdk'
import { useI18n } from 'vue-i18n'
import ModelSelect from '@/pages/bots/components/model-select.vue'

const { t } = useI18n()
const queryCache = useQueryCache()
const saveLoading = ref(false)
const embeddingModelId = ref('')

const { data: configData } = useQuery({
  key: () => ['memory-config'],
  query: async () => {
    const { data } = await getMemoryConfig({ throwOnError: true })
    return data
  },
})

const { data: modelData } = useQuery({
  key: () => ['models'],
  query: async () => {
    const { data } = await getModels({ throwOnError: true })
    return data
  },
})

const { data: providerData } = useQuery({
  key: () => ['providers'],
  query: async () => {
    const { data } = await getProviders({ throwOnError: true })
    return data
  },
})

const models = computed(() => modelData.value ?? [])
const providers = computed(() => providerData.value ?? [])

const savedEmbeddingModelId = computed(() => configData.value?.embedding_model_id ?? '')
const hasChanges = computed(() =>
  configData.value !== undefined && embeddingModelId.value.trim() !== savedEmbeddingModelId.value,
)

// embeddingModelId is shared between the user's draft and the server snapshot
// that colada's refetchOnWindowFocus can replace at any moment — the
// composable reconciles them (hard reset once the config first loads, guard
// otherwise).
useServerSyncedScalar(embeddingModelId, {
  source: () => configData.value,
  identity: config => (config === undefined ? 'loading' : 'builtin'),
  server: config => config?.embedding_model_id ?? '',
})

async function handleSave() {
  saveLoading.value = true
  try {
    await putMemoryConfig({
      body: { embedding_model_id: embeddingModelId.value.trim() },
      throwOnError: true,
    })
    toast.success(t('memory.saveSuccess'))
    queryCache.invalidateQueries({ key: ['memory-config'] })
  } catch (error) {
    console.error('Failed to save memory config:', error)
    toast.error(t('common.saveFailed'))
  } finally {
    saveLoading.value = false
  }
}

defineExpose({ hasChanges, saveLoading, save: handleSave })
</script>
