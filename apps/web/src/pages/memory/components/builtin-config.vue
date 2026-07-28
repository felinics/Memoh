<template>
  <!-- Built-in backend: one settings card holding the embedding-model row.
       The provider's configured state needs no stat tiles — the mode is always
       graph (the only mode the built-in runtime saves) and semantic readiness
       is fully derived from whether this row's model is set. -->
  <SectionGroup :title="$t('memory.providerNames.builtin')">
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
import { computed, ref, watch } from 'vue'
import { SectionGroup, SettingsRow, SettingsSection, toast } from '@felinic/ui'
import { useQuery, useQueryCache } from '@pinia/colada'
import {
  getModels,
  getProviders,
  postMemoryProviders,
  putMemoryProvidersById,
} from '@memohai/sdk'
import type { AdaptersProviderGetResponse } from '@memohai/sdk'
import { useI18n } from 'vue-i18n'
import ModelSelect from '@/pages/bots/components/model-select.vue'

const props = defineProps<{
  provider?: AdaptersProviderGetResponse | null
}>()

const { t } = useI18n()
const queryCache = useQueryCache()
const saveLoading = ref(false)
const embeddingModelId = ref('')

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

const savedEmbeddingModelId = computed(() => {
  const config = (props.provider?.config ?? {}) as Record<string, unknown>
  return typeof config.embedding_model_id === 'string' ? config.embedding_model_id : ''
})
const hasChanges = computed(() => {
  const config = (props.provider?.config ?? {}) as Record<string, unknown>
  if (!props.provider?.id || config.memory_mode !== 'graph') return true
  return embeddingModelId.value.trim() !== savedEmbeddingModelId.value
})

watch(() => props.provider, (provider) => {
  const config = (provider?.config ?? {}) as Record<string, unknown>
  embeddingModelId.value = typeof config.embedding_model_id === 'string' ? config.embedding_model_id : ''
}, { immediate: true })

async function handleSave() {
  saveLoading.value = true
  try {
    const config: Record<string, unknown> = { memory_mode: 'graph' }
    if (embeddingModelId.value.trim()) {
      config.embedding_model_id = embeddingModelId.value.trim()
    }
    if (props.provider?.id) {
      await putMemoryProvidersById({
        path: { id: props.provider.id },
        body: { name: props.provider.name ?? 'Built-in', config },
        throwOnError: true,
      })
    } else {
      await postMemoryProviders({
        body: { name: 'Built-in', provider: 'builtin', config },
        throwOnError: true,
      })
    }
    toast.success(t('memory.saveSuccess'))
    queryCache.invalidateQueries({ key: ['memory-providers'] })
  } catch (error) {
    console.error('Failed to save memory provider:', error)
    toast.error(t('common.saveFailed'))
  } finally {
    saveLoading.value = false
  }
}

defineExpose({ hasChanges, saveLoading, save: handleSave })
</script>
