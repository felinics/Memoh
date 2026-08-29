<template>
  <PageShell :title="$t('supermarket.title')">
    <template #actions>
      <Button
        variant="outline"
        as="a"
        href="https://github.com/felinics/supermarket"
        target="_blank"
        rel="noopener noreferrer"
      >
        <Github class="size-4" />
        {{ $t('supermarket.submit') }}
      </Button>
    </template>

    <div class="space-y-6">
      <!-- Search -->
      <div class="relative">
        <Search class="absolute left-3 top-1/2 -translate-y-1/2 size-3.5 text-muted-foreground" />
        <Input
          v-model="searchInput"
          :placeholder="$t(capabilitiesStore.connectors
            ? 'supermarket.searchPlaceholderWithConnectors'
            : 'supermarket.searchPlaceholder')"
          class="pl-9"
          @keydown.enter="applySearch"
        />
      </div>

      <InlineLoadingRow
        v-if="!capabilitiesStore.loaded"
        class="justify-center py-8"
      >
        {{ $t('common.loading') }}
      </InlineLoadingRow>

      <Tabs
        v-else
        v-model="activeTab"
        class="w-full"
      >
        <TabsList>
          <TabsTrigger
            v-if="capabilitiesStore.connectors"
            value="connectors"
          >
            {{ $t('supermarket.connectorsSection') }}
          </TabsTrigger>
          <TabsTrigger value="skills">
            {{ $t('supermarket.skillsSection') }}
          </TabsTrigger>
        </TabsList>

        <!-- Skills Tab -->
        <TabsContent
          value="skills"
          class="space-y-4"
        >
          <SegmentedControl
            v-if="registryFilterItems.length > 1"
            :model-value="selectedRegistry"
            :items="registryFilterItems"
            :aria-label="$t('supermarket.registryFilter')"
            class="w-full sm:w-fit"
            @update:model-value="onRegistryFilterChange"
          />

          <InlineLoadingRow
            v-if="packagesLoading"
            class="justify-center py-8"
          >
            {{ $t('common.loading') }}
          </InlineLoadingRow>

          <div
            v-else-if="!packages.length"
            class="py-8 text-center text-xs text-muted-foreground"
          >
            {{ $t('supermarket.noPackageResults') }}
          </div>

          <div
            v-else
            class="grid grid-cols-1 gap-4 sm:grid-cols-2"
          >
            <PackageCard
              v-for="pkg in packages"
              :key="`${pkg.registry_id}/${pkg.package_id}`"
              :pkg="pkg"
            />
          </div>

          <div
            v-if="showPagination"
            class="flex justify-end gap-2"
          >
            <Button
              variant="outline"
              size="icon-sm"
              :disabled="page === 1 || packagesLoading"
              :aria-label="$t('supermarket.previousPage')"
              @click="page--"
            >
              <ChevronLeft class="size-4" />
            </Button>
            <Button
              variant="outline"
              size="icon-sm"
              :disabled="!hasNextPage || packagesLoading"
              :aria-label="$t('supermarket.nextPage')"
              @click="page++"
            >
              <ChevronRight class="size-4" />
            </Button>
          </div>
        </TabsContent>

        <TabsContent
          v-if="capabilitiesStore.connectors"
          value="connectors"
        >
          <InlineLoadingRow
            v-if="connectorsQuery.isLoading.value"
            class="justify-center py-8"
          >
            {{ $t('common.loading') }}
          </InlineLoadingRow>

          <div
            v-else-if="searchQuery && !filteredConnectors.length"
            class="py-8 text-center text-xs text-muted-foreground"
          >
            {{ $t('supermarket.noConnectorResults') }}
          </div>

          <Empty
            v-else-if="!filteredConnectors.length"
            class="py-12"
          >
            <EmptyHeader>
              <EmptyTitle>{{ $t('connectors.catalogEmptyTitle') }}</EmptyTitle>
              <EmptyDescription>{{ $t('connectors.catalogEmptyDescription') }}</EmptyDescription>
            </EmptyHeader>
          </Empty>

          <div
            v-else
            class="grid grid-cols-1 gap-4 sm:grid-cols-2"
          >
            <MarketItemCard
              v-for="connector in filteredConnectors"
              :key="connector.type"
              :name="connector.name || connector.type"
              :description="connector.description"
              :homepage="connector.homepage_url"
              @open="openConnectorConnect(connector)"
            >
              <template #leading>
                <ProviderIcon
                  :icon="connector.icon_url || ''"
                  size="20"
                  class="size-5 object-contain"
                >
                  <Plug class="size-4 text-muted-foreground" />
                </ProviderIcon>
              </template>
              <template #actions>
                <Button
                  size="sm"
                  :disabled="connector.status !== 'ready'"
                  @click="openConnectorConnect(connector)"
                >
                  {{ connector.status === 'ready'
                    ? $t('connectors.connect')
                    : $t('connectors.unavailable') }}
                </Button>
              </template>
            </MarketItemCard>
          </div>
        </TabsContent>
      </Tabs>

      <ConnectConnectorDialog
        v-model:open="connectorDialogOpen"
        :connector="selectedConnector"
        :default-bot-id="defaultBotId"
        @connected="openBotConnectors"
      />
    </div>
  </PageShell>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useQuery } from '@pinia/colada'
import { ChevronLeft, ChevronRight, Github, Plug, Search } from 'lucide-vue-next'
import {
  Button,
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
  InlineLoadingRow,
  Input,
  PageShell,
  SegmentedControl,
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
  toast,
  type SegmentedItem,
} from '@felinic/ui'
import {
  getConnectorsCatalog,
  getSupermarketRegistries,
  getSupermarketPackages,
  type ConnectitConnector,
  type HandlersSupermarketSkillPackageSummary,
  type HandlersSupermarketRegistry,
} from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'
import PackageCard from './components/package-card.vue'
import ConnectConnectorDialog from './components/connect-connector-dialog.vue'
import MarketItemCard from './components/market-item-card.vue'
import ProviderIcon from '@/components/provider-icon/index.vue'
import { useSyncedQueryParam } from '@/composables/useSyncedQueryParam'
import { useCapabilitiesStore } from '@/store/capabilities'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const capabilitiesStore = useCapabilitiesStore()
const tabParam = useSyncedQueryParam('tab', '')
const pageSize = 50
const allRegistriesValue = 'all'
// Settings pages are KeepAlive-cached and share the `tab` query key, so a
// foreign value (e.g. bot detail's ?tab=memory) can land in the synced param
// while this page is deactivated. Render anything outside this page's own
// tabs as the capability-dependent default; writes go back through the synced param.
const activeTab = computed({
  get: () => {
    const valid = capabilitiesStore.connectors
      ? ['connectors', 'skills']
      : ['skills']
    const fallback = capabilitiesStore.connectors ? 'connectors' : 'skills'
    return valid.includes(tabParam.value) ? tabParam.value : fallback
  },
  set: (value: string) => {
    tabParam.value = value
  },
})

const searchInput = ref('')
const searchQuery = ref('')
const page = ref(1)
const total = ref(0)
const selectedRegistry = ref(allRegistriesValue)
const packages = ref<HandlersSupermarketSkillPackageSummary[]>([])
const registries = ref<HandlersSupermarketRegistry[]>([])
const packagesLoading = ref(false)

const connectorDialogOpen = ref(false)
const selectedConnector = ref<ConnectitConnector | null>(null)

const hasNextPage = computed(() => page.value * pageSize < total.value)
const showPagination = computed(() => page.value > 1 || hasNextPage.value)
const registryFilterItems = computed<SegmentedItem[]>(() => [
  { value: allRegistriesValue, label: t('supermarket.allRegistries') },
  ...registries.value
    .filter((registry): registry is HandlersSupermarketRegistry & { id: string } => !!registry.id)
    .map(registry => ({
      value: registry.id,
      label: registry.name || registry.id,
    })),
])

const defaultBotId = computed(() => {
  const value = route.query.botId
  return typeof value === 'string' ? value : ''
})

const connectorsQuery = useQuery({
  key: () => ['connectors-catalog'],
  query: async () => {
    const { data } = await getConnectorsCatalog({ throwOnError: true })
    return data
  },
  enabled: () => capabilitiesStore.connectors,
})

const filteredConnectors = computed(() => {
  const query = searchQuery.value.toLowerCase()
  const connectors = connectorsQuery.data.value ?? []
  if (!query) return connectors
  return connectors.filter(connector =>
    [connector.name, connector.type, connector.description, ...(connector.categories ?? [])]
      .some(value => value?.toLowerCase().includes(query)),
  )
})

watch(connectorsQuery.error, error => {
  if (error) toast.error(resolveApiErrorMessage(error, t('supermarket.loadError')))
})

watch(
  () => [capabilitiesStore.loaded, capabilitiesStore.connectors] as const,
  ([loaded, connectors]) => {
    // Normalize the URL param only while this page owns the current route —
    // firing while KeepAlive-deactivated would rewrite another page's ?tab.
    if (loaded && !connectors && route.name === 'supermarket' && tabParam.value === 'connectors') {
      tabParam.value = 'skills'
    }
  },
  { immediate: true },
)

onMounted(() => {
  void capabilitiesStore.load()
})

function applySearch() {
  const nextQuery = searchInput.value.trim()
  if (searchQuery.value === nextQuery) {
    page.value = 1
    void refreshAll()
    return
  }
  searchQuery.value = nextQuery
}

let searchDebounce: ReturnType<typeof setTimeout> | undefined
watch(searchInput, () => {
  clearTimeout(searchDebounce)
  searchDebounce = setTimeout(applySearch, 300)
})

function onRegistryFilterChange(value: string | number) {
  const next = String(value)
  if (selectedRegistry.value === next) return
  selectedRegistry.value = next
  if (page.value !== 1) {
    page.value = 1
    return
  }
  void loadPackages()
}

function openConnectorConnect(connector: ConnectitConnector) {
  if (connector.status !== 'ready') return
  selectedConnector.value = connector
  connectorDialogOpen.value = true
}

function openBotConnectors(botId: string) {
  void router.push({
    name: 'bot-detail',
    params: { botName: botId },
    query: { tab: 'connectors' },
  })
}

async function loadRegistries() {
  try {
    const { data } = await getSupermarketRegistries({ throwOnError: true })
    registries.value = data.data ?? []
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('supermarket.loadError')))
  }
}

async function loadPackages() {
  packagesLoading.value = true
  try {
    const { data } = await getSupermarketPackages({
      query: {
        q: searchQuery.value || undefined,
        registry: selectedRegistry.value === allRegistriesValue
          ? undefined
          : selectedRegistry.value,
        page: page.value,
        limit: pageSize,
        sort: 'relevance',
      },
      throwOnError: true,
    })
    packages.value = data.data ?? []
    total.value = data.total ?? 0
  } catch (error) {
    packages.value = []
    total.value = 0
    toast.error(resolveApiErrorMessage(error, t('supermarket.loadError')))
  } finally {
    packagesLoading.value = false
  }
}

function refreshAll() {
  void loadPackages()
}

watch(searchQuery, () => {
  if (page.value !== 1) {
    page.value = 1
    return
  }
  refreshAll()
})

watch(page, loadPackages)

void loadRegistries()
refreshAll()
</script>
