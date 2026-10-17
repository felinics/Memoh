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
import { getModels, getProviders } from '@memohai/sdk'
import type { ModelsGetResponse, ProvidersGetResponse } from '@memohai/sdk'
import { Boxes, Box, ChevronRight, Plus } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { BackendCard, DetailPane, PageShell, SwapTransition } from '@felinic/ui'
import AddProvider from '@/components/add-provider/index.vue'
import BackendCardGridSkeleton from '@/components/backend-card-grid-skeleton/index.vue'
import ProviderIcon from '@/components/provider-icon/index.vue'
import { useRoutedViewSwap } from '@/composables/useViewSwap'
import { avatarInitials } from '@/composables/useAvatarInitials'
import {
  isMonochromeLogo,
  providerLogo,
  MODEL_SOURCES,
  modelSourceLogo,
  providerCatalogEntry,
  providerDraftFromSource,
  type ModelSource,
} from '@/constants/model-sources'
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

const curProvider = ref<ProvidersGetResponse>()
const curSource = ref<ModelSource>()
const optimisticProvider = ref<ProvidersGetResponse>()
const addOpen = ref(false)

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

// Entries already behind a provider: one created from the entry, or from the
// provider template or preset the entry replaced.
const configuredEntries = computed(() => {
  const configured = new Set<string>()
  for (const provider of catalogProviders.value) {
    const entry = providerCatalogEntry(provider as { metadata?: Record<string, unknown> })
    if (entry) configured.add(entry.id)
  }
  return configured
})

const availableSources = computed(() => MODEL_SOURCES.filter(item => !configuredEntries.value.has(item.id)))

type ProviderDetail =
  // routeAlias lets a provider answer for another route value.
  | { kind: 'provider', provider: ProvidersGetResponse, routeAlias?: string }
  | { kind: 'source', source: ModelSource }

// A provider just created from a draft drops its entry from the gallery before
// the route has moved from the draft to the provider; until it moves, the
// provider answers for the draft's route so the detail stays open.
const materializedAlias = computed<ProviderDetail | undefined>(() => {
  const provider = optimisticProvider.value
  const entry = providerCatalogEntry(provider as { metadata?: Record<string, unknown> } | undefined)
  return provider && entry ? { kind: 'provider', provider, routeAlias: `source:${entry.id}` } : undefined
})

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
    ...(materializedAlias.value ? [materializedAlias.value] : []),
    ...availableSources.value.map(source => ({ kind: 'source' as const, source })),
  ],
  selected: () => {
    if (curSource.value) return { kind: 'source', source: curSource.value }
    return curProvider.value ? { kind: 'provider', provider: curProvider.value } : undefined
  },
  select: (detail) => {
    curSource.value = detail?.kind === 'source' ? detail.source : undefined
    curProvider.value = detail?.kind === 'provider'
      ? detail.provider
      : detail?.kind === 'source'
        ? providerDraftFromSource(detail.source) as ProvidersGetResponse
        : undefined
  },
  getRouteValue: detail => detail.kind === 'provider'
    ? (detail.routeAlias ?? detail.provider.id ?? '')
    : `source:${detail.source.id}`,
  isLoading: () => providersLoading.value,
  isReady: () => providerData.value !== undefined,
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

// Keep cached cards mounted during background refreshes. Only an unresolved
// query still fetching needs a skeleton; a failed one must leave loading.
const listLoading = computed(() => providerData.value === undefined && providersLoading.value)

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
    || item.id.toLowerCase().includes(keyword)
    || item.base_url.toLowerCase().includes(keyword),
  )
})

type GalleryCard =
  | { kind: 'provider', key: string, provider: ProvidersGetResponse }
  | { kind: 'source', key: string, source: ModelSource }

// Configured providers come first, then the entries still on offer.
const galleryCards = computed<GalleryCard[]>(() => [
  ...filteredProviders.value.map(provider => ({ kind: 'provider' as const, key: provider.id ?? '', provider })),
  ...filteredSources.value.map(source => ({ kind: 'source' as const, key: `source:${source.id}`, source })),
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
  return item.base_url.replace(/^https?:\/\//, '') || item.client_type
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

function openSource(item: ModelSource) {
  openProvider({ kind: 'source', source: item })
}

function openProviderInstance(provider: ProvidersGetResponse) {
  openProvider({ kind: 'provider', provider })
}

function handleMaterialized(provider: ProvidersGetResponse) {
  optimisticProvider.value = provider
  openProvider({ kind: 'provider', provider })
}

function openAddProvider() {
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
        v-else-if="catalogProviders.length + availableSources.length > 0"
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
                    v-if="providerLogo(card.provider)"
                    :icon="providerLogo(card.provider)!"
                    :monochrome="isMonochromeLogo(providerLogo(card.provider))"
                    size="1.5em"
                  >
                    <span class="text-xs font-medium text-muted-foreground">
                      {{ avatarInitials(card.provider.name, '?') }}
                    </span>
                  </ProviderIcon>
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
        :sources="MODEL_SOURCES"
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
