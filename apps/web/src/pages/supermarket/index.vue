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

    <div class="space-y-8">
      <div class="space-y-4">
        <div class="relative">
          <Search class="absolute left-3 top-1/2 -translate-y-1/2 size-3.5 text-muted-foreground" />
          <Input
            v-model="searchInput"
            :placeholder="$t('supermarket.searchPlaceholder')"
            class="pl-9"
            @keydown.enter="applySearch"
          />
        </div>

        <SegmentedControl
          v-if="registryFilterItems.length > 1"
          :model-value="selectedRegistry"
          :items="registryFilterItems"
          :aria-label="$t('supermarket.registryFilter')"
          class="w-full sm:w-fit"
          @update:model-value="onRegistryFilterChange"
        />
      </div>

      <!-- Searching flattens the catalog into one paginated list; browsing
           groups it by category, a preview per category with "View all"
           leading to the category page when there is more. -->
      <template v-if="searching">
        <InlineLoadingRow
          v-if="searchLoading"
          class="justify-center py-8"
        >
          {{ $t('common.loading') }}
        </InlineLoadingRow>

        <div
          v-else-if="!searchResults.length"
          class="py-8 text-center text-xs text-muted-foreground"
        >
          {{ $t('supermarket.noAppResults') }}
        </div>

        <div
          v-else
          class="grid grid-cols-1 gap-4 sm:grid-cols-2"
        >
          <AppCard
            v-for="pkg in searchResults"
            :key="`${pkg.registry_id}/${pkg.app_id}`"
            :pkg="pkg"
            :bot-id="defaultBotId"
          />
        </div>

        <div
          v-if="showPagination"
          class="flex justify-end gap-2"
        >
          <Button
            variant="outline"
            size="icon-sm"
            :disabled="page === 1 || searchLoading"
            :aria-label="$t('supermarket.previousPage')"
            @click="page--"
          >
            <ChevronLeft class="size-4" />
          </Button>
          <Button
            variant="outline"
            size="icon-sm"
            :disabled="!hasNextPage || searchLoading"
            :aria-label="$t('supermarket.nextPage')"
            @click="page++"
          >
            <ChevronRight class="size-4" />
          </Button>
        </div>
      </template>

      <template v-else>
        <InlineLoadingRow
          v-if="sectionsLoading && !sections.length"
          class="justify-center py-8"
        >
          {{ $t('common.loading') }}
        </InlineLoadingRow>

        <div
          v-else-if="!sections.length"
          class="py-8 text-center text-xs text-muted-foreground"
        >
          {{ $t('supermarket.noAppResults') }}
        </div>

        <section
          v-for="section in sections"
          :key="section.category.id"
          class="space-y-3"
        >
          <div class="flex items-center justify-between gap-3">
            <h2 class="text-base font-semibold">
              {{ sectionTitle(section) }}
            </h2>
            <Button
              v-if="section.total > SECTION_PREVIEW_LIMIT"
              variant="ghost"
              size="sm"
              @click="openCategory(section.category.id)"
            >
              {{ $t('supermarket.viewAll') }}
              <ChevronRight class="size-4" />
            </Button>
          </div>
          <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <AppCard
              v-for="pkg in section.apps"
              :key="`${pkg.registry_id}/${pkg.app_id}`"
              :pkg="pkg"
              :bot-id="defaultBotId"
            />
          </div>
        </section>
      </template>
    </div>
  </PageShell>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ChevronLeft, ChevronRight, Github, Search } from 'lucide-vue-next'
import {
  Button,
  InlineLoadingRow,
  Input,
  PageShell,
  SegmentedControl,
  toast,
  type SegmentedItem,
} from '@felinic/ui'
import {
  getSupermarketApps,
  getSupermarketRegistries,
  type HandlersSupermarketAppCategory,
  type HandlersSupermarketRegistry,
  type HandlersSupermarketAppSummary,
} from '@memohai/sdk'
import { categoryDisplayName, useAppCategoriesQuery } from '@/composables/api/useApps'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { browsableCategories, SECTION_PREVIEW_LIMIT } from './category-sections'
import AppCard from './components/app-card.vue'

interface CategorySection {
  category: HandlersSupermarketAppCategory
  apps: HandlersSupermarketAppSummary[]
  total: number
}

const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()
const pageSize = 50
const allValue = 'all'

const searchInput = ref('')
const searchQuery = ref('')
const page = ref(1)
const total = ref(0)
const selectedRegistry = ref(allValue)
const registries = ref<HandlersSupermarketRegistry[]>([])
const searchResults = ref<HandlersSupermarketAppSummary[]>([])
const searchLoading = ref(false)
const sections = ref<CategorySection[]>([])
const sectionsLoading = ref(false)

const categoriesQuery = useAppCategoriesQuery()
const categories = computed(() => categoriesQuery.data.value ?? [])

const searching = computed(() => !!searchQuery.value)
const registryParam = computed(() => (selectedRegistry.value === allValue ? undefined : selectedRegistry.value))
const hasNextPage = computed(() => page.value * pageSize < total.value)
const showPagination = computed(() => page.value > 1 || hasNextPage.value)
const registryFilterItems = computed<SegmentedItem[]>(() => [
  { value: allValue, label: t('supermarket.allRegistries') },
  ...registries.value
    .filter((registry): registry is HandlersSupermarketRegistry & { id: string } => !!registry.id)
    .map(registry => ({ value: registry.id, label: registry.name || registry.id })),
])

const defaultBotId = computed(() => {
  const value = route.query.botId
  return typeof value === 'string' ? value : ''
})

function sectionTitle(section: CategorySection): string {
  return categoryDisplayName(section.category, section.category.name, locale.value)
}

function openCategory(categoryId: string) {
  router.push({
    name: 'supermarket-category',
    params: { categoryId },
    query: {
      ...(registryParam.value ? { registry: registryParam.value } : {}),
      ...(defaultBotId.value ? { botId: defaultBotId.value } : {}),
    },
  })
}

function applySearch() {
  const nextQuery = searchInput.value.trim()
  if (searchQuery.value === nextQuery) {
    if (nextQuery) {
      page.value = 1
      void loadSearch()
    }
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
}

async function loadRegistries() {
  try {
    const { data } = await getSupermarketRegistries({ throwOnError: true })
    registries.value = data.data ?? []
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('supermarket.loadError')))
  }
}

// Requests overlap when the query or registry changes quickly; only the
// newest one may write its result.
let searchSequence = 0
async function loadSearch() {
  const sequence = ++searchSequence
  searchLoading.value = true
  try {
    const { data } = await getSupermarketApps({
      query: {
        q: searchQuery.value,
        registry: registryParam.value,
        page: page.value,
        limit: pageSize,
        sort: 'relevance',
      },
      throwOnError: true,
    })
    if (sequence !== searchSequence) return
    searchResults.value = data.data ?? []
    total.value = data.total ?? 0
  } catch (error) {
    if (sequence !== searchSequence) return
    searchResults.value = []
    total.value = 0
    toast.error(resolveApiErrorMessage(error, t('supermarket.loadError')))
  } finally {
    if (sequence === searchSequence) searchLoading.value = false
  }
}

let sectionsSequence = 0
async function loadSections() {
  const sequence = ++sectionsSequence
  const visible = browsableCategories(categories.value, registryParam.value ?? '')
  if (!visible.length) {
    sections.value = []
    sectionsLoading.value = false
    return
  }
  sectionsLoading.value = true
  try {
    const loaded = await Promise.all(visible.map(async (category): Promise<CategorySection> => {
      const { data } = await getSupermarketApps({
        query: {
          registry: registryParam.value,
          category: category.id,
          page: 1,
          limit: SECTION_PREVIEW_LIMIT,
          sort: 'relevance',
        },
        throwOnError: true,
      })
      return { category, apps: data.data ?? [], total: data.total ?? 0 }
    }))
    if (sequence !== sectionsSequence) return
    sections.value = loaded.filter(section => section.apps.length > 0)
  } catch (error) {
    if (sequence !== sectionsSequence) return
    sections.value = []
    toast.error(resolveApiErrorMessage(error, t('supermarket.loadError')))
  } finally {
    if (sequence === sectionsSequence) sectionsLoading.value = false
  }
}

watch([searchQuery, selectedRegistry], () => {
  if (searching.value) {
    if (page.value !== 1) {
      page.value = 1
      return
    }
    void loadSearch()
  }
})
watch(page, () => {
  if (searching.value) void loadSearch()
})
watch([categories, selectedRegistry], () => {
  void loadSections()
}, { immediate: true })
watch(categoriesQuery.error, (error) => {
  if (error) toast.error(resolveApiErrorMessage(error, t('supermarket.loadError')))
})

void loadRegistries()
</script>
