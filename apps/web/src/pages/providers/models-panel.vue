<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useQuery } from '@pinia/colada'
import {
  Button,
  Empty,
  Pagination,
  PaginationContent,
  PaginationEllipsis,
  PaginationFirst,
  PaginationItem,
  PaginationLast,
  PaginationNext,
  PaginationPrevious,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@felinic/ui'
import { getModels, getProviders, getProviderTemplates } from '@memohai/sdk'
import type { ModelsGetResponse, ProvidersGetResponse, ProvidertemplatesGetResponse } from '@memohai/sdk'
import { Boxes, Box, ChevronRight, Plus } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { BackendCard, DetailPane, PageShell, SwapTransition } from '@felinic/ui'
import AddProvider from '@/components/add-provider/index.vue'
import BackendCardGridSkeleton from '@/components/backend-card-grid-skeleton/index.vue'
import ProviderIcon from '@/components/provider-icon/index.vue'
import { useRoutedViewSwap } from '@/composables/useViewSwap'
import { avatarInitials } from '@/composables/useAvatarInitials'
import { isTemplateConfigured, providerDraftFromTemplate, templateDefaultConfig } from '@/utils/provider-template'
import { isMonochromeLogo, MODEL_SOURCES, TEMPLATE_SOURCES, modelSourceLogo, type ModelSource } from '@/constants/model-sources'
import ModelSetting from './model-setting.vue'

const { t } = useI18n()

const { data: providerData, isLoading: providersLoading } = useQuery({
  key: () => ['providers'],
  query: async () => {
    const { data } = await getProviders({ throwOnError: true })
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

const { data: templateData, isLoading: templatesLoading } = useQuery({
  key: () => ['provider-templates', 'llm'],
  query: async () => {
    const { data } = await getProviderTemplates({ query: { domain: 'llm' }, throwOnError: true })
    return data
  },
})

const curProvider = ref<ProvidersGetResponse>()
const curTemplate = ref<ProvidertemplatesGetResponse>()
const optimisticProvider = ref<ProvidersGetResponse>()
const addOpen = ref(false)
const initialTemplateId = ref('')
const initialSource = ref('')

// The search box + Add button live in the container page's fixed header (same
// row for every scope); this panel receives the query and exposes its add
// dialog trigger instead of rendering its own actions row.
const props = defineProps<{ searchQuery: string }>()
defineExpose({ openAdd: () => openAddProvider() })

const providers = computed<ProvidersGetResponse[]>(() => {
  if (!Array.isArray(providerData.value)) return []
  return [...providerData.value].sort((a, b) => {
    const ae = a.enable !== false ? 1 : 0
    const be = b.enable !== false ? 1 : 0
    return be - ae
  })
})

const templates = computed<ProvidertemplatesGetResponse[]>(() =>
  Array.isArray(templateData.value) ? templateData.value : [],
)

// Templates a models.dev source replaces are offered as that source instead.
const availableTemplates = computed(() => templates.value.filter(template =>
  !isTemplateConfigured(template)
  && template.id !== optimisticProvider.value?.provider_template_id
  && !TEMPLATE_SOURCES[template.key ?? ''],
))

// Sources already behind a provider: one created from the source, or from the
// template or preset the source replaced.
const configuredSources = computed(() => {
  const templateKeys = new Map(templates.value.map(template => [template.id, template.key ?? '']))
  const configured = new Set<string>()
  for (const provider of catalogProviders.value) {
    const metadata = (provider.metadata ?? {}) as Record<string, unknown>
    if (typeof metadata.source === 'string') configured.add(metadata.source)
    const presetSource = (metadata.preset as { source?: unknown } | undefined)?.source
    const key = provider.provider_template_id
      ? templateKeys.get(provider.provider_template_id)
      : typeof presetSource === 'string' ? presetSource.replace(/\.yaml$/, '') : ''
    const source = key ? TEMPLATE_SOURCES[key] : undefined
    if (source) configured.add(source)
  }
  return configured
})

const availableSources = computed(() => MODEL_SOURCES.filter(item => !configuredSources.value.has(item.source)))

const catalogProviders = computed(() => {
  const provider = optimisticProvider.value
  if (!provider?.id || providers.value.some(item => item.id === provider.id)) return providers.value
  return [provider, ...providers.value]
})

watch(providers, (items) => {
  if (optimisticProvider.value?.id && items.some(item => item.id === optimisticProvider.value?.id)) {
    optimisticProvider.value = undefined
  }
})

type ProviderDetail =
  | { kind: 'provider', provider: ProvidersGetResponse }
  | { kind: 'template', template: ProvidertemplatesGetResponse }

// Page-owned query key (unique under settings KeepAlive — see useViewSwap.ts).
const {
  view,
  direction,
  isDetailLoading,
  openDetail: openProvider,
  backToList: closeProvider,
} = useRoutedViewSwap<ProviderDetail>({
  key: 'provider',
  items: () => [
    ...catalogProviders.value.map(provider => ({ kind: 'provider' as const, provider })),
    ...availableTemplates.value.map(template => ({ kind: 'template' as const, template })),
  ],
  selected: () => {
    if (curTemplate.value) return { kind: 'template', template: curTemplate.value }
    return curProvider.value ? { kind: 'provider', provider: curProvider.value } : undefined
  },
  select: (detail) => {
    curTemplate.value = detail?.kind === 'template' ? detail.template : undefined
    curProvider.value = detail?.kind === 'provider'
      ? detail.provider
      : detail?.kind === 'template'
        ? providerDraftFromTemplate(detail.template)
        : undefined
  },
  getRouteValue: detail => detail.kind === 'provider'
    ? detail.provider.provider_template_id
      ? `template:${detail.provider.provider_template_id}`
      : (detail.provider.id ?? '')
    : `template:${detail.template.id}`,
  isLoading: routeValue => routeValue.startsWith('template:')
    ? templatesLoading.value || providersLoading.value
    : providersLoading.value,
  isReady: routeValue => routeValue.startsWith('template:')
    ? templateData.value !== undefined && providerData.value !== undefined
    : providerData.value !== undefined,
})

const modelCountByProvider = computed(() => {
  const counts: Record<string, number> = {}
  for (const model of (modelData.value as ModelsGetResponse[] | undefined) ?? []) {
    const id = model.provider_id
    if (!id) continue
    counts[id] = (counts[id] ?? 0) + 1
  }
  return counts
})

// Keep cached cards mounted during background refreshes. Only unresolved
// queries still fetching need a skeleton; failed queries must leave loading.
const listLoading = computed(() =>
  (providerData.value === undefined && providersLoading.value)
  || (templateData.value === undefined && templatesLoading.value),
)

const filteredProviders = computed(() => {
  const keyword = props.searchQuery.trim().toLowerCase()
  if (!keyword) return catalogProviders.value
  return catalogProviders.value.filter((p) => {
    const name = (p.name ?? '').toLowerCase()
    const url = providerSubtitle(p).toLowerCase()
    return name.includes(keyword) || url.includes(keyword)
  })
})

const filteredSources = computed(() => {
  const keyword = props.searchQuery.trim().toLowerCase()
  if (!keyword) return availableSources.value
  return availableSources.value.filter(item =>
    item.name.toLowerCase().includes(keyword)
    || item.source.toLowerCase().includes(keyword)
    || item.base_url.toLowerCase().includes(keyword),
  )
})

const filteredTemplates = computed(() => {
  const keyword = props.searchQuery.trim().toLowerCase()
  if (!keyword) return availableTemplates.value
  return availableTemplates.value.filter(template =>
    (template.name ?? '').toLowerCase().includes(keyword)
    || (template.driver ?? '').toLowerCase().includes(keyword)
    || (template.description ?? '').toLowerCase().includes(keyword),
  )
})

type GalleryCard =
  | { kind: 'provider', key: string, provider: ProvidersGetResponse }
  | { kind: 'template', key: string, template: ProvidertemplatesGetResponse }
  | { kind: 'source', key: string, source: ModelSource }

// Configured providers come first, then the templates and sources still on offer.
const galleryCards = computed<GalleryCard[]>(() => [
  ...filteredProviders.value.map(provider => ({ kind: 'provider' as const, key: provider.id ?? '', provider })),
  ...filteredTemplates.value.map(template => ({ kind: 'template' as const, key: `template:${template.id}`, template })),
  ...filteredSources.value.map(source => ({ kind: 'source' as const, key: `source:${source.source}`, source })),
])

const PAGE_SIZE = 20
const currentPage = ref(1)
const pageCount = computed(() => Math.max(1, Math.ceil(galleryCards.value.length / PAGE_SIZE)))
const pagedCards = computed(() => {
  const start = (currentPage.value - 1) * PAGE_SIZE
  return galleryCards.value.slice(start, start + PAGE_SIZE)
})

watch(() => props.searchQuery, () => {
  currentPage.value = 1
})
watch(pageCount, (count) => {
  if (currentPage.value > count) currentPage.value = count
})

function sourceSubtitle(item: ModelSource) {
  return item.base_url.replace(/^https?:\/\//, '')
}

function providerSubtitle(provider: ProvidersGetResponse) {
  const baseUrl = (provider.config as Record<string, unknown> | undefined)?.base_url
  if (typeof baseUrl === 'string' && baseUrl) {
    return baseUrl.replace(/^https?:\/\//, '')
  }
  return provider.client_type ?? ''
}

function modelCount(id: string | undefined) {
  return id ? (modelCountByProvider.value[id] ?? 0) : 0
}

function templateSubtitle(template: ProvidertemplatesGetResponse) {
  const baseUrl = templateDefaultConfig(template).base_url
  if (typeof baseUrl === 'string' && baseUrl) {
    return baseUrl.replace(/^https?:\/\//, '')
  }
  return template.driver ?? ''
}

function openTemplate(template: ProvidertemplatesGetResponse) {
  openProvider({ kind: 'template', template })
}

function openProviderInstance(provider: ProvidersGetResponse) {
  openProvider({ kind: 'provider', provider })
}

function handleMaterialized(provider: ProvidersGetResponse) {
  optimisticProvider.value = provider
  openProvider({ kind: 'provider', provider })
}

function openAddProvider(templateId?: string) {
  initialTemplateId.value = templateId ?? ''
  initialSource.value = ''
  addOpen.value = true
}

function openSource(item: ModelSource) {
  initialTemplateId.value = ''
  initialSource.value = item.source
  addOpen.value = true
}
</script>

<template>
  <SwapTransition :direction="direction">
    <!-- Gallery. No title here: the Providers container page owns the fixed
         H1 and the scope rail; panels only supply their actions (the framed
         PageHeader row keeps its min-h-9 so switching scopes never jumps). -->
    <PageShell
      v-if="view === 'list'"
      variant="tab"
      class="px-4 md:px-6"
    >
      <BackendCardGridSkeleton v-if="listLoading" />

      <div
        v-else-if="catalogProviders.length + availableTemplates.length + availableSources.length > 0"
        class="flex flex-col gap-4"
      >
        <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <template
            v-for="card in pagedCards"
            :key="card.key"
          >
            <BackendCard
              v-if="card.kind === 'provider'"
              :name="card.provider.name ?? ''"
              :subtitle="providerSubtitle(card.provider)"
              :enabled="card.provider.enable !== false"
              @click="openProviderInstance(card.provider)"
            >
              <template #leading>
                <span class="flex size-10 items-center justify-center rounded-full bg-muted">
                  <ProviderIcon
                    v-if="card.provider.icon"
                    :icon="card.provider.icon"
                    :monochrome="isMonochromeLogo(card.provider.icon)"
                    size="1.5em"
                  />
                  <span
                    v-else
                    class="text-xs font-medium text-muted-foreground"
                  >
                    {{ avatarInitials(card.provider.name, '?') }}
                  </span>
                </span>
              </template>
              <template #trailing>
                <span
                  v-if="modelCount(card.provider.id) > 0"
                  class="flex shrink-0 items-center gap-1 text-xs text-muted-foreground"
                >
                  <Boxes class="size-3.5" />
                  {{ modelCount(card.provider.id) }}
                </span>
                <ChevronRight
                  v-else
                  class="size-4 shrink-0 text-muted-foreground/60"
                />
              </template>
            </BackendCard>

            <BackendCard
              v-else-if="card.kind === 'template'"
              :name="card.template.name ?? ''"
              :subtitle="templateSubtitle(card.template)"
              @click="openTemplate(card.template)"
            >
              <template #leading>
                <span class="flex size-10 items-center justify-center rounded-full bg-muted">
                  <ProviderIcon
                    v-if="card.template.icon"
                    :icon="card.template.icon"
                    size="1.5em"
                  />
                  <span
                    v-else
                    class="text-xs font-medium text-muted-foreground"
                  >
                    {{ avatarInitials(card.template.name, '?') }}
                  </span>
                </span>
              </template>
            </BackendCard>

            <BackendCard
              v-else
              :name="card.source.name"
              :subtitle="sourceSubtitle(card.source)"
              @click="openSource(card.source)"
            >
              <template #leading>
                <span class="flex size-10 items-center justify-center rounded-full bg-muted">
                  <ProviderIcon
                    :icon="modelSourceLogo(card.source)"
                    :monochrome="isMonochromeLogo(modelSourceLogo(card.source))"
                    size="1.5em"
                  >
                    <span class="text-xs font-medium text-muted-foreground">
                      {{ avatarInitials(card.source.name, '?') }}
                    </span>
                  </ProviderIcon>
                </span>
              </template>
            </BackendCard>
          </template>
        </div>

        <Pagination
          v-if="pageCount > 1"
          :total="galleryCards.length"
          :items-per-page="PAGE_SIZE"
          :sibling-count="1"
          :page="currentPage"
          show-edges
          @update:page="currentPage = $event"
        >
          <PaginationContent v-slot="{ items }">
            <PaginationFirst />
            <PaginationPrevious />
            <template
              v-for="(item, index) in items"
              :key="index"
            >
              <PaginationEllipsis
                v-if="item.type === 'ellipsis'"
                :index="index"
              />
              <PaginationItem
                v-else
                :value="item.value"
                :is-active="item.value === currentPage"
              />
            </template>
            <PaginationNext />
            <PaginationLast />
          </PaginationContent>
        </Pagination>
      </div>

      <Empty
        v-else
        class="rounded-[var(--radius-menu-shell)] border border-dashed border-border py-16"
      >
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <Box />
          </EmptyMedia>
        </EmptyHeader>
        <EmptyTitle>{{ t('provider.emptyTitle') }}</EmptyTitle>
        <EmptyDescription>{{ t('provider.emptyDescription') }}</EmptyDescription>
        <EmptyContent>
          <Button
            variant="outline"
            @click="openAddProvider()"
          >
            <Plus class="size-4" />
            {{ t('provider.addBtn') }}
          </Button>
        </EmptyContent>
      </Empty>

      <AddProvider
        v-model:open="addOpen"
        :providers="providers"
        :templates="templates"
        :initial-template-id="initialTemplateId"
        :sources="MODEL_SOURCES"
        :initial-source="initialSource"
        hide-trigger
      />
    </PageShell>

    <!-- Detail -->
    <DetailPane
      v-else
      width="narrow"
      :back-label="t('models.title')"
      :loading="isDetailLoading || !curProvider"
      @back="closeProvider"
    >
      <ModelSetting
        v-if="curProvider"
        v-model:provider="curProvider"
        @materialized="handleMaterialized"
      />
    </DetailPane>
  </SwapTransition>
</template>
