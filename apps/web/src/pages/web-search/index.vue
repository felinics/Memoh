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
import { BackendCard, DetailPane, PageShell, SectionGroup, SwapTransition } from '@felinic/ui'
import FetchProviderSetting from './components/fetch-provider-setting.vue'
import ProviderSetting from './components/provider-setting.vue'
import SearchProviderLogo from '@/components/search-provider-logo/index.vue'
import { useRoutedViewSwap } from '@/composables/useViewSwap'
import { providerConfigDefaults } from '@/utils/provider-template'

const { t } = useI18n()

const SEARCH_PROVIDER_TYPES = ['brave', 'bing', 'google', 'tavily', 'sogou', 'serper', 'searxng', 'jina', 'exa', 'bocha', 'duckduckgo', 'yandex'] as const
const FETCH_PROVIDER_TYPES = ['native', 'jina', 'cloudflare_markdown'] as const

const { data: providerData, isLoading: providersLoading } = useQuery({
  key: () => ['search-providers'],
  query: async () => {
    const { data } = await getSearchProviders({ throwOnError: true })
    return data
  },
})

const { data: fetchProviderData, isLoading: fetchProvidersLoading } = useQuery({
  key: () => ['fetch-providers'],
  query: async () => {
    const { data } = await getFetchProviders({ throwOnError: true })
    return data
  },
})

const { data: fetchProviderMetaData } = useQuery({
  key: () => ['fetch-providers-meta'],
  query: async () => {
    const { data } = await getFetchProvidersMeta({ throwOnError: true })
    return data
  },
})

const { data: searchProviderMetaData } = useQuery({
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

const searchItems = computed<SearchprovidersGetResponse[]>(() => SEARCH_PROVIDER_TYPES.map((provider) => {
  const meta = searchProviderMetas.value.find(item => item.provider === provider)
  return providers.value.find(item => item.provider === provider)
    ?? optimisticSearchProviders.value[provider]
    ?? {
      name: meta?.display_name ?? t(`webSearch.providerNames.${provider}`, provider),
      provider,
      enable: false,
      config: providerConfigDefaults(meta?.config_schema),
    }
}))

const fetchProviders = computed<FetchprovidersGetResponse[]>(() =>
  Array.isArray(fetchProviderData.value) ? fetchProviderData.value : [],
)
const fetchItems = computed<FetchprovidersGetResponse[]>(() => FETCH_PROVIDER_TYPES.map((provider) => {
  const meta = fetchProviderMetas.value.find(item => item.provider === provider)
  return fetchProviders.value.find(item => item.provider === provider)
    ?? optimisticFetchProviders.value[provider]
    ?? {
      name: meta?.display_name ?? t(`webSearch.fetchProviderNames.${provider}`, provider),
      provider,
      enable: provider === 'native',
      config: providerConfigDefaults(meta?.config_schema),
    }
}))

// Page-owned query key, valued `kind:id` so refresh restores which pane.
const {
  view,
  direction,
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
    if (routeValue.startsWith('search:')) return providersLoading.value
    if (routeValue.startsWith('fetch:')) return fetchProvidersLoading.value
    return false
  },
  isReady: (routeValue) => {
    if (routeValue.startsWith('search:')) return providerData.value !== undefined
    if (routeValue.startsWith('fetch:')) return fetchProviderData.value !== undefined
    return true
  },
})

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
      :title="t('webSearch.title')"
    >
      <div class="space-y-8">
        <!-- Search providers -->
        <SectionGroup
          :title="t('webSearch.searchProviders')"
          :description="t('webSearch.searchHint')"
        >
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <BackendCard
              v-for="provider in searchItems"
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
        </SectionGroup>

        <!-- Fetch providers -->
        <SectionGroup
          :title="t('webSearch.fetchProviders')"
          :description="t('webSearch.fetchHint')"
        >
          <div
            v-if="fetchItems.length > 0"
            class="grid grid-cols-1 gap-3 sm:grid-cols-2"
          >
            <BackendCard
              v-for="provider in fetchItems"
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
      :loading="isDetailLoading || !(detailKind === 'search' ? curProvider : curFetchProvider)"
      @back="closeProvider"
    >
      <ProviderSetting
        v-if="detailKind === 'search' && curProvider"
        @materialized="handleSearchMaterialized"
      />
      <FetchProviderSetting
        v-else-if="detailKind === 'fetch' && curFetchProvider"
        @materialized="handleFetchMaterialized"
      />
    </DetailPane>
  </SwapTransition>
</template>
