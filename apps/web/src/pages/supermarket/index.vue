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
      <div class="relative">
        <Search class="absolute left-3 top-1/2 -translate-y-1/2 size-3.5 text-muted-foreground" />
        <Input
          v-model="searchInput"
          :placeholder="$t('supermarket.searchPlaceholder')"
          class="pl-9"
          @keydown.enter="applySearch"
        />
      </div>

      <!-- Everything is a Package: the filters narrow by where it comes from,
           what it is for, and which components it carries. -->
      <div class="flex flex-wrap items-center gap-3">
        <SegmentedControl
          v-if="registryFilterItems.length > 1"
          :model-value="selectedRegistry"
          :items="registryFilterItems"
          :aria-label="$t('supermarket.registryFilter')"
          class="w-full sm:w-fit"
          @update:model-value="onRegistryFilterChange"
        />
        <SegmentedControl
          :model-value="selectedComponent"
          :items="componentFilterItems"
          :aria-label="$t('supermarket.componentFilter')"
          class="w-full sm:w-fit"
          @update:model-value="onComponentFilterChange"
        />
        <Select
          :model-value="selectedCategory"
          @update:model-value="onCategoryChange"
        >
          <SelectTrigger class="w-full sm:w-48">
            <SelectValue :placeholder="$t('supermarket.allCategories')" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem :value="allValue">
              {{ $t('supermarket.allCategories') }}
            </SelectItem>
            <SelectItem
              v-for="category in categories"
              :key="category.id"
              :value="category.id"
            >
              {{ categoryDisplayName(category, category.name, locale) }}
              <span class="ml-1 text-caption text-muted-foreground">{{ category.package_count }}</span>
            </SelectItem>
          </SelectContent>
        </Select>
      </div>

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
          :category-label="categoryLabel(pkg)"
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
    </div>
  </PageShell>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ChevronLeft, ChevronRight, Github, Search } from 'lucide-vue-next'
import {
  Button,
  InlineLoadingRow,
  Input,
  PageShell,
  SegmentedControl,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  toast,
  type SegmentedItem,
} from '@felinic/ui'
import {
  getSupermarketPackages,
  getSupermarketRegistries,
  type HandlersSupermarketRegistry,
  type HandlersSupermarketSkillPackageSummary,
} from '@memohai/sdk'
import { categoryDisplayName, usePackageCategoriesQuery } from '@/composables/api/usePackages'
import { resolveApiErrorMessage } from '@/utils/api-error'
import PackageCard from './components/package-card.vue'

type ComponentFilter = 'all' | 'skills' | 'dependencies' | 'connectors'

const { t, locale } = useI18n()
const route = useRoute()
const pageSize = 50
const allValue = 'all'

const searchInput = ref('')
const searchQuery = ref('')
const page = ref(1)
const total = ref(0)
const selectedRegistry = ref(allValue)
const selectedComponent = ref<ComponentFilter>('all')
const selectedCategory = ref(allValue)
const packages = ref<HandlersSupermarketSkillPackageSummary[]>([])
const registries = ref<HandlersSupermarketRegistry[]>([])
const packagesLoading = ref(false)

const categoriesQuery = usePackageCategoriesQuery()
const categories = computed(() => categoriesQuery.data.value ?? [])
const categoryById = computed(() => new Map(categories.value.map(category => [category.id, category])))

const hasNextPage = computed(() => page.value * pageSize < total.value)
const showPagination = computed(() => page.value > 1 || hasNextPage.value)
const registryFilterItems = computed<SegmentedItem[]>(() => [
  { value: allValue, label: t('supermarket.allRegistries') },
  ...registries.value
    .filter((registry): registry is HandlersSupermarketRegistry & { id: string } => !!registry.id)
    .map(registry => ({ value: registry.id, label: registry.name || registry.id })),
])
const componentFilterItems = computed<SegmentedItem[]>(() => [
  { value: 'all', label: t('supermarket.component.all') },
  { value: 'skills', label: t('supermarket.component.skills') },
  { value: 'dependencies', label: t('supermarket.component.dependencies') },
  { value: 'connectors', label: t('supermarket.component.connectors') },
])

const defaultBotId = computed(() => {
  const value = route.query.botId
  return typeof value === 'string' ? value : ''
})

function categoryLabel(pkg: HandlersSupermarketSkillPackageSummary): string {
  return categoryDisplayName(categoryById.value.get(pkg.category), pkg.category_name, locale.value)
}

function applySearch() {
  const nextQuery = searchInput.value.trim()
  if (searchQuery.value === nextQuery) {
    page.value = 1
    void loadPackages()
    return
  }
  searchQuery.value = nextQuery
}

let searchDebounce: ReturnType<typeof setTimeout> | undefined
watch(searchInput, () => {
  clearTimeout(searchDebounce)
  searchDebounce = setTimeout(applySearch, 300)
})

function resetToFirstPage() {
  if (page.value !== 1) {
    page.value = 1
    return
  }
  void loadPackages()
}

function onRegistryFilterChange(value: string | number) {
  const next = String(value)
  if (selectedRegistry.value === next) return
  selectedRegistry.value = next
  resetToFirstPage()
}

function onComponentFilterChange(value: string | number) {
  const next = String(value) as ComponentFilter
  if (selectedComponent.value === next) return
  selectedComponent.value = next
  resetToFirstPage()
}

function onCategoryChange(value: unknown) {
  const next = typeof value === 'string' && value ? value : allValue
  if (selectedCategory.value === next) return
  selectedCategory.value = next
  resetToFirstPage()
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
        registry: selectedRegistry.value === allValue ? undefined : selectedRegistry.value,
        category: selectedCategory.value === allValue ? undefined : selectedCategory.value,
        component: selectedComponent.value === 'all' ? undefined : selectedComponent.value,
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

watch(searchQuery, resetToFirstPage)
watch(page, loadPackages)

void loadRegistries()
void loadPackages()
</script>
