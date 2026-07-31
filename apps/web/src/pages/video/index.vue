<script setup lang="ts">
import { computed, provide, reactive, ref, watch } from 'vue'
import { useQuery, useQueryCache } from '@pinia/colada'
import { BackendCard, Button, DetailPane, PageShell, SwapTransition } from '@felinic/ui'
import { getProviderTemplates, getVideoProviders, postVideoProvidersByIdImportModels } from '@memohai/sdk'
import type { ProvidersGetResponse, ProvidertemplatesGetResponse, VideoProviderResponse } from '@memohai/sdk'
import { Plus } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import AddProvider from '@/components/add-provider/index.vue'
import ProviderIcon from '@/components/provider-icon/index.vue'
import { useRoutedViewSwap } from '@/composables/useViewSwap'
import VideoProviderSetting from './provider-setting.vue'
import { isTemplateConfigured, providerDraftFromTemplate } from '@/utils/provider-template'

const { t } = useI18n()
const queryCache = useQueryCache()

const { data: providersData, isLoading: providersLoading } = useQuery({
  key: () => ['video-providers'],
  query: async () => {
    const { data } = await getVideoProviders({ throwOnError: true })
    return data
  },
})

const { data: templateData, isLoading: templatesLoading } = useQuery({
  key: () => ['provider-templates', 'video'],
  query: async () => {
    const { data } = await getProviderTemplates({ query: { domain: 'video' }, throwOnError: true })
    return data
  },
})

type TemplateVideoProvider = VideoProviderResponse & { provider_template_id?: string }
const curProvider = ref<TemplateVideoProvider>()
const optimisticProvider = ref<TemplateVideoProvider>()
provide('curVideoProvider', curProvider)

const openStatus = reactive({ addOpen: false })
const initialTemplateId = ref('')

const providers = computed<VideoProviderResponse[]>(() => {
  const list = Array.isArray(providersData.value) ? providersData.value : []
  return [...list].sort((a, b) => Number(b.enable !== false) - Number(a.enable !== false))
})
const templates = computed<ProvidertemplatesGetResponse[]>(() =>
  Array.isArray(templateData.value) ? templateData.value : [],
)
const availableTemplates = computed(() => templates.value.filter(template =>
  !isTemplateConfigured(template)
  && template.id !== optimisticProvider.value?.provider_template_id,
))
const catalogProviders = computed<TemplateVideoProvider[]>(() => {
  const provider = optimisticProvider.value
  if (!provider?.id || providers.value.some(item => item.id === provider.id)) return providers.value
  return [provider, ...providers.value]
})
const templateDrafts = computed<TemplateVideoProvider[]>(() =>
  availableTemplates.value.map(template => providerDraftFromTemplate(template) as TemplateVideoProvider),
)

// Page-owned query key (unique under settings KeepAlive — see useViewSwap.ts).
const {
  view,
  direction,
  isDetailLoading,
  openDetail: openProvider,
  backToList: closeProvider,
} = useRoutedViewSwap<TemplateVideoProvider>({
  key: 'videoProvider',
  items: () => [...catalogProviders.value, ...templateDrafts.value],
  selected: () => curProvider.value,
  select: provider => curProvider.value = provider,
  getRouteValue: provider => provider.provider_template_id
    ? `template:${provider.provider_template_id}`
    : provider.id ?? '',
  isLoading: routeValue => routeValue.startsWith('template:')
    ? templatesLoading.value || providersLoading.value
    : providersLoading.value,
  isReady: routeValue => routeValue.startsWith('template:')
    ? templateData.value !== undefined && providersData.value !== undefined
    : providersData.value !== undefined,
})

const addProviderNames = computed(() => catalogProviders.value.map((p) => ({ name: p.name })))

function getInitials(name: string | undefined) {
  const label = name?.trim() ?? ''
  return label ? label.slice(0, 2).toUpperCase() : '?'
}

async function importVideoModels(providerId: string) {
  const { data } = await postVideoProvidersByIdImportModels({
    path: { id: providerId },
    throwOnError: true,
  })
  queryCache.invalidateQueries({ key: ['video-providers'] })
  queryCache.invalidateQueries({ key: ['video-models'] })
  queryCache.invalidateQueries({ key: ['video-provider-models', providerId] })
  return data
}

function openAddProvider(templateId?: string) {
  initialTemplateId.value = templateId ?? ''
  openStatus.addOpen = true
}

function handleMaterialized(provider: ProvidersGetResponse) {
  const result = provider as TemplateVideoProvider
  optimisticProvider.value = result
  openProvider(result)
}

watch(() => openStatus.addOpen, (isOpen, wasOpen) => {
  if (wasOpen && !isOpen) {
    queryCache.invalidateQueries({ key: ['video-providers'] })
    queryCache.invalidateQueries({ key: ['video-models'] })
  }
})
</script>

<template>
  <SwapTransition :direction="direction">
    <!-- Single provider group — same shell as the providers gallery: PageShell owns
         the title, the hint (as its description), and the Add action; the body is a
         bare BackendCard grid. NOT a SectionGroup — that owner is only for pages that
         stack SEVERAL provider groups (voice TTS/STT, web-search search/fetch). The
         page-level "视频生成" heading was dropped: title + description already say it. -->
    <PageShell
      v-if="view === 'list'"
      :title="t('video.title')"
      :description="t('video.providersHint')"
    >
      <template #actions>
        <Button @click="openAddProvider()">
          <Plus class="size-4" />
          {{ t('common.add') }}
        </Button>
      </template>

      <div
        v-if="catalogProviders.length + templateDrafts.length > 0"
        class="grid grid-cols-1 gap-3 sm:grid-cols-2"
      >
        <BackendCard
          v-for="provider in catalogProviders"
          :key="provider.id"
          :name="provider.name ?? ''"
          :enabled="provider.enable !== false"
          @click="openProvider(provider)"
        >
          <template #leading>
            <span class="flex size-10 items-center justify-center rounded-full bg-muted">
              <ProviderIcon
                v-if="provider.icon"
                :icon="provider.icon"
                size="1.5em"
              />
              <span
                v-else
                class="text-xs font-medium text-muted-foreground"
              >
                {{ getInitials(provider.name) }}
              </span>
            </span>
          </template>
        </BackendCard>
        <BackendCard
          v-for="provider in templateDrafts"
          :key="`template:${provider.provider_template_id}`"
          :name="provider.name ?? ''"
          @click="openProvider(provider)"
        >
          <template #leading>
            <span class="flex size-10 items-center justify-center rounded-full bg-muted">
              <ProviderIcon
                v-if="provider.icon"
                :icon="provider.icon"
                size="1.5em"
              />
              <span
                v-else
                class="text-xs font-medium text-muted-foreground"
              >
                {{ getInitials(provider.name) }}
              </span>
            </span>
          </template>
        </BackendCard>
      </div>
      <p
        v-else
        class="px-2 text-xs text-muted-foreground"
      >
        {{ t('video.empty') }}
      </p>

      <AddProvider
        v-model:open="openStatus.addOpen"
        hide-trigger
        preset-domain="video"
        :providers="addProviderNames"
        :templates="templates"
        :initial-template-id="initialTemplateId"
        :import-models="importVideoModels"
      />
    </PageShell>

    <DetailPane
      v-else
      width="narrow"
      :back-label="t('video.title')"
      :loading="isDetailLoading || !curProvider"
      @back="closeProvider"
    >
      <VideoProviderSetting
        v-if="curProvider"
        @materialized="handleMaterialized"
      />
    </DetailPane>
  </SwapTransition>
</template>
