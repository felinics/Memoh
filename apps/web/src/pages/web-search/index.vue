<script setup lang="ts">
import { computed, provide, ref } from 'vue'
import { useQuery } from '@pinia/colada'
import { getFetchProviders, getFetchProvidersMeta, getSearchProviders, getSearchProvidersMeta } from '@memohai/sdk'
import type {
  FetchprovidersGetResponse,
  FetchprovidersProviderMeta,
  SearchprovidersGetResponse,
  SearchprovidersProviderMeta,
} from '@memohai/sdk'
import { useI18n } from 'vue-i18n'
import { BackendCard, CalloutBanner, DetailPane, PageShell, SectionGroup, SettingsShell, SwapTransition } from '@felinic/ui'
import BackendCardGridSkeleton from '@/components/backend-card-grid-skeleton/index.vue'
import LoadingButton from '@/components/loading-button/index.vue'
import FetchProviderSetting from './components/fetch-provider-setting.vue'
import ProviderSetting from './components/provider-setting.vue'
import SearchProviderLogo from '@/components/search-provider-logo/index.vue'
import { useRoutedViewSwap } from '@/composables/useViewSwap'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { providerConfigDefaults } from '@/utils/provider-template'

const { t } = useI18n()

const SEARCH_PROVIDER_TYPES = ['brave', 'bing', 'google', 'tavily', 'sogou', 'serper', 'searxng', 'jina', 'exa', 'bocha', 'duckduckgo', 'yandex', 'deepseek'] as const
const FETCH_PROVIDER_TYPES = ['native', 'jina', 'cloudflare_markdown'] as const

const {
  data: providerData,
  isLoading: providersLoading,
  error: providersError,
  refetch: refetchSearchProviders,
} = useQuery({
  key: () => ['search-providers'],
  query: async () => {
    const { data } = await getSearchProviders({ throwOnError: true })
    return data
  },
})

const {
  data: fetchProviderData,
  isLoading: fetchProvidersLoading,
  error: fetchProvidersError,
  refetch: refetchFetchProviders,
} = useQuery({
  key: () => ['fetch-providers'],
  query: async () => {
    const { data } = await getFetchProviders({ throwOnError: true })
    return data
  },
})

const {
  data: fetchProviderMetaData,
  isLoading: fetchProviderMetaLoading,
  error: fetchProviderMetaError,
  refetch: refetchFetchProviderMeta,
} = useQuery({
  key: () => ['fetch-providers-meta'],
  query: async () => {
    const { data } = await getFetchProvidersMeta({ throwOnError: true })
    return data
  },
})

const {
  data: searchProviderMetaData,
  isLoading: searchProviderMetaLoading,
  error: searchProviderMetaError,
  refetch: refetchSearchProviderMeta,
} = useQuery({
  key: () => ['search-providers-meta'],
  query: async () => {
    const { data } = await getSearchProvidersMeta({ throwOnError: true })
    return data
  },
})

const curProvider = ref<SearchprovidersGetResponse>()
const curFetchProvider = ref<FetchprovidersGetResponse>()
const optimisticSearchProviders = ref<Record<string, SearchprovidersGetResponse>>({})
const optimisticFetchProviders = ref<Record<string, FetchprovidersGetResponse>>({})
provide('curSearchProvider', curProvider)
provide('curFetchProvider', curFetchProvider)

type WebDetailKind = 'search' | 'fetch'
type WebDetail =
  | { kind: 'search', provider: SearchprovidersGetResponse }
  | { kind: 'fetch', provider: FetchprovidersGetResponse }
const detailKind = ref<WebDetailKind>('search')

function sortByEnabled<T extends { enable?: boolean }>(list: T[]) {
  return [...list].sort((a, b) => Number(b.enable !== false) - Number(a.enable !== false))
}

const providers = computed<SearchprovidersGetResponse[]>(() =>
  Array.isArray(providerData.value) ? sortByEnabled(providerData.value) : [],
)
const searchProviderMetas = computed<SearchprovidersProviderMeta[]>(() =>
  Array.isArray(searchProviderMetaData.value) ? searchProviderMetaData.value : [],
)
const fetchProviderMetas = computed<FetchprovidersProviderMeta[]>(() =>
  Array.isArray(fetchProviderMetaData.value) ? fetchProviderMetaData.value : [],
)

const searchReady = computed(() => providerData.value !== undefined && searchProviderMetaData.value !== undefined)
const fetchReady = computed(() => fetchProviderData.value !== undefined && fetchProviderMetaData.value !== undefined)

// Gate synthesized placeholders until both the server list and its schema meta
// resolve: rendering placeholders before `providerData` and `*-meta` settle
// flashes disabled cards, breaks meta-driven provider catalogs, and lets
// deep-linked detail routes mount without credentials or field schemas.
const searchItems = computed<SearchprovidersGetResponse[]>(() => {
  if (!searchReady.value) return []
  return SEARCH_PROVIDER_TYPES.map((provider) => {
    const meta = searchProviderMetas.value.find(item => item.provider === provider)
    return providers.value.find(item => item.provider === provider)
      ?? optimisticSearchProviders.value[provider]
      ?? {
        name: meta?.display_name ?? t(`webSearch.providerNames.${provider}`, provider),
        provider,
        enable: false,
        config: providerConfigDefaults(meta?.config_schema),
      }
  })
})

const fetchProviders = computed<FetchprovidersGetResponse[]>(() =>
  Array.isArray(fetchProviderData.value) ? fetchProviderData.value : [],
)
const fetchItems = computed<FetchprovidersGetResponse[]>(() => {
  if (!fetchReady.value) return []
  return FETCH_PROVIDER_TYPES.map((provider) => {
    const meta = fetchProviderMetas.value.find(item => item.provider === provider)
    return fetchProviders.value.find(item => item.provider === provider)
      ?? optimisticFetchProviders.value[provider]
      ?? {
        name: meta?.display_name ?? t(`webSearch.fetchProviderNames.${provider}`, provider),
        provider,
        enable: provider === 'native',
        config: providerConfigDefaults(meta?.config_schema),
      }
  })
})

const searchError = computed(() =>
  (providerData.value === undefined ? providersError.value : null)
  ?? (searchProviderMetaData.value === undefined ? searchProviderMetaError.value : null),
)
const fetchError = computed(() =>
  (fetchProviderData.value === undefined ? fetchProvidersError.value : null)
  ?? (fetchProviderMetaData.value === undefined ? fetchProviderMetaError.value : null),
)
const searchBusy = computed(() => providersLoading.value || searchProviderMetaLoading.value)
const fetchBusy = computed(() => fetchProvidersLoading.value || fetchProviderMetaLoading.value)
const searchLoadFailed = computed(() => Boolean(searchError.value))
const fetchLoadFailed = computed(() => Boolean(fetchError.value))
const searchInitialLoading = computed(() => !searchReady.value && searchBusy.value && !searchLoadFailed.value)
const fetchInitialLoading = computed(() => !fetchReady.value && fetchBusy.value && !fetchLoadFailed.value)

// The search box lives in the Providers container's fixed header and pipes the
// query in as a prop; this panel only filters. (No Add button for this scope —
// the list IS the catalog: clicking a template card starts configuring it.)
const props = defineProps<{ searchQuery: string }>()

function byName<T extends { name?: string }>(list: T[]): T[] {
  const keyword = props.searchQuery.trim().toLowerCase()
  if (!keyword) return list
  return list.filter(p => (p.name ?? '').toLowerCase().includes(keyword))
}

const filteredSearchItems = computed(() => byName(searchItems.value))
const filteredFetchItems = computed(() => byName(fetchItems.value))

// Page-owned query key, valued `kind:id` so refresh restores which pane.
const {
  view,
  direction,
  queryValue: activeRouteValue,
  isDetailLoading,
  openDetail,
  backToList: closeProvider,
} = useRoutedViewSwap<WebDetail>({
  key: 'webProvider',
  items: () => [
    ...searchItems.value.map(provider => ({ kind: 'search' as const, provider })),
    ...fetchItems.value.map(provider => ({ kind: 'fetch' as const, provider })),
  ],
  selected: () => {
    if (detailKind.value === 'search' && curProvider.value) {
      return { kind: 'search', provider: curProvider.value }
    }
    if (detailKind.value === 'fetch' && curFetchProvider.value) {
      return { kind: 'fetch', provider: curFetchProvider.value }
    }
    return undefined
  },
  select: (detail) => {
    detailKind.value = detail?.kind ?? 'search'
    curProvider.value = detail?.kind === 'search' ? detail.provider : undefined
    curFetchProvider.value = detail?.kind === 'fetch' ? detail.provider : undefined
  },
  getRouteValue: detail => `${detail.kind}:${detail.provider.provider}`,
  isLoading: (routeValue) => {
    if (routeValue.startsWith('search:')) return searchBusy.value
    if (routeValue.startsWith('fetch:')) return fetchBusy.value
    return false
  },
  isReady: (routeValue) => {
    if (routeValue.startsWith('search:')) return searchReady.value
    if (routeValue.startsWith('fetch:')) return fetchReady.value
    return true
  },
})

const activeDetailKind = computed<WebDetailKind>(() => {
  if (activeRouteValue.value.startsWith('fetch:')) return 'fetch'
  if (activeRouteValue.value.startsWith('search:')) return 'search'
  return detailKind.value
})

const detailLoadError = computed(() => (activeDetailKind.value === 'fetch' ? fetchError.value : searchError.value))
const detailLoadFailed = computed(() => Boolean(detailLoadError.value))
const detailRetrying = computed(() => (activeDetailKind.value === 'fetch' ? fetchBusy.value : searchBusy.value))

function retrySearchLoad() {
  if (providerData.value === undefined) void refetchSearchProviders()
  if (searchProviderMetaData.value === undefined) void refetchSearchProviderMeta()
}

function retryFetchLoad() {
  if (fetchProviderData.value === undefined) void refetchFetchProviders()
  if (fetchProviderMetaData.value === undefined) void refetchFetchProviderMeta()
}

function retryDetailLoad() {
  if (activeDetailKind.value === 'fetch') retryFetchLoad()
  else retrySearchLoad()
}

function loadErrorDescription(err: unknown): string {
  const fallback = t('common.loadFailed')
  const message = resolveApiErrorMessage(err, fallback)
  return message !== fallback ? message : ''
}

function openProvider(provider: SearchprovidersGetResponse) {
  openDetail({ kind: 'search', provider })
}

function openFetchProvider(provider: FetchprovidersGetResponse) {
  openDetail({ kind: 'fetch', provider })
}

function handleSearchMaterialized(provider: SearchprovidersGetResponse) {
  if (!provider.provider) return
  optimisticSearchProviders.value = {
    ...optimisticSearchProviders.value,
    [provider.provider]: provider,
  }
  curProvider.value = provider
}

function handleFetchMaterialized(provider: FetchprovidersGetResponse) {
  if (!provider.provider) return
  optimisticFetchProviders.value = {
    ...optimisticFetchProviders.value,
    [provider.provider]: provider,
  }
  curFetchProvider.value = provider
}
</script>

<template>
  <SwapTransition :direction="direction">
    <!-- Two provider sections (search + fetch). Twin of the voice/video provider
         pages: PageShell title bar + SectionGroup per provider kind. -->
    <PageShell
      v-if="view === 'list'"
      variant="tab"
      class="px-4 md:px-6"
    >
      <div class="space-y-8">
        <!-- Search providers -->
        <SectionGroup
          :title="t('webSearch.searchProviders')"
          :description="t('webSearch.searchHint')"
        >
          <BackendCardGridSkeleton v-if="searchInitialLoading" />
          <CalloutBanner
            v-else-if="searchLoadFailed"
            tone="destructive"
            :title="t('common.loadFailed')"
            :description="loadErrorDescription(searchError)"
          >
            <LoadingButton
              type="button"
              variant="outline"
              size="sm"
              :loading="searchBusy"
              @click="retrySearchLoad()"
            >
              {{ t('common.retry') }}
            </LoadingButton>
          </CalloutBanner>
          <div
            v-else-if="filteredSearchItems.length > 0"
            class="grid grid-cols-1 gap-3 sm:grid-cols-2"
          >
            <BackendCard
              v-for="provider in filteredSearchItems"
              :key="provider.provider"
              :name="provider.name ?? ''"
              :enabled="provider.enable !== false && !!provider.id"
              @click="openProvider(provider)"
            >
              <template #leading>
                <span class="flex size-10 items-center justify-center">
                  <SearchProviderLogo
                    :provider="provider.provider || ''"
                    size="md"
                  />
                </span>
              </template>
            </BackendCard>
          </div>
          <p
            v-else
            class="px-2 text-xs text-muted-foreground"
          >
            {{ t('webSearch.empty') }}
          </p>
        </SectionGroup>

        <!-- Fetch providers -->
        <SectionGroup
          :title="t('webSearch.fetchProviders')"
          :description="t('webSearch.fetchHint')"
        >
          <BackendCardGridSkeleton
            v-if="fetchInitialLoading"
            :count="3"
          />
          <CalloutBanner
            v-else-if="fetchLoadFailed"
            tone="destructive"
            :title="t('common.loadFailed')"
            :description="loadErrorDescription(fetchError)"
          >
            <LoadingButton
              type="button"
              variant="outline"
              size="sm"
              :loading="fetchBusy"
              @click="retryFetchLoad()"
            >
              {{ t('common.retry') }}
            </LoadingButton>
          </CalloutBanner>
          <div
            v-else-if="filteredFetchItems.length > 0"
            class="grid grid-cols-1 gap-3 sm:grid-cols-2"
          >
            <BackendCard
              v-for="provider in filteredFetchItems"
              :key="provider.provider"
              :name="provider.name ?? ''"
              :enabled="provider.enable !== false && (provider.provider === 'native' || !!provider.id)"
              @click="openFetchProvider(provider)"
            >
              <template #leading>
                <span class="flex size-10 items-center justify-center">
                  <SearchProviderLogo
                    :provider="provider.provider || ''"
                    size="md"
                  />
                </span>
              </template>
            </BackendCard>
          </div>
          <p
            v-else
            class="px-2 text-xs text-muted-foreground"
          >
            {{ t('webSearch.emptyFetch') }}
          </p>
        </SectionGroup>
      </div>
    </PageShell>

    <!-- Provider detail -->
    <DetailPane
      v-else
      width="narrow"
      :back-label="t('webSearch.title')"
      :loading="!detailLoadFailed && (isDetailLoading || !(activeDetailKind === 'search' ? curProvider : curFetchProvider))"
      @back="closeProvider"
    >
      <SettingsShell
        v-if="detailLoadFailed"
        width="narrow"
      >
        <CalloutBanner
          tone="destructive"
          :title="t('common.loadFailed')"
          :description="loadErrorDescription(detailLoadError)"
        >
          <LoadingButton
            type="button"
            variant="outline"
            size="sm"
            :loading="detailRetrying"
            @click="retryDetailLoad"
          >
            {{ t('common.retry') }}
          </LoadingButton>
        </CalloutBanner>
      </SettingsShell>
      <ProviderSetting
        v-else-if="detailKind === 'search' && curProvider"
        @materialized="handleSearchMaterialized"
      />
      <FetchProviderSetting
        v-else-if="detailKind === 'fetch' && curFetchProvider"
        @materialized="handleFetchMaterialized"
      />
    </DetailPane>
  </SwapTransition>
</template>
