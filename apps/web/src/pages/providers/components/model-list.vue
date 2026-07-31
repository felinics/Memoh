<template>
  <SettingsSection :title="$t('models.title')">
    <!-- Toolbar sits in its own padded block; the model rows below are flush
         list rows with an inset hairline between them (same language as the
         Configuration card), not nested cards. No rule under the toolbar —
         spacing alone separates it from the first row. -->
    <div class="flex items-center gap-2 p-4">
      <InputGroup
        v-if="searchVisible"
        size="sm"
        class="min-w-0 flex-1"
      >
        <InputGroupAddon align="inline-start">
          <Search class="size-4 text-muted-foreground" />
        </InputGroupAddon>
        <InputGroupInput
          v-model="searchQuery"
          :placeholder="$t('models.searchModelPlaceholder')"
        />
      </InputGroup>
      <div
        v-if="providerId"
        class="ml-auto flex items-center gap-2"
      >
        <ImportModelsDialog
          :provider-id="providerId"
          size="sm"
          :mode="(models?.length ?? 0) > 0 ? 'refresh' : 'import'"
        />
        <CreateModel
          v-if="!managed"
          :id="providerId"
          size="sm"
        />
      </div>
    </div>

    <template v-if="models && models.length > 0">
      <div
        v-if="displayedModels.length > 0"
        class="pb-2"
      >
        <ModelItem
          v-for="model in displayedModels"
          :key="model.id || `${model.provider_id}:${model.model_id}`"
          :model="model"
          :delete-loading="deleteModelLoading"
          :search-aligned="searchVisible"
          :managed="managed"
          :preview="preview"
          @edit="(model) => $emit('edit', model)"
          @delete="(id) => $emit('delete', id)"
        />
      </div>

      <div
        v-if="totalPages > 1"
        class="flex items-center justify-between border-t border-border px-4 py-3"
      >
        <span class="text-xs text-muted-foreground whitespace-nowrap">
          {{ $t('models.showingCount', { count: `${pageStart}-${pageEnd}`, total: filteredModels.length }) }}
        </span>
        <Pagination
          :total="filteredModels.length"
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
        v-if="filteredModels.length === 0"
        class="flex flex-col items-center justify-center px-4 py-10"
      >
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <Search />
          </EmptyMedia>
        </EmptyHeader>
        <EmptyTitle>{{ $t('models.searchNoResults') }}</EmptyTitle>
      </Empty>
    </template>

    <Empty
      v-else
      class="flex flex-col items-center justify-center px-4 py-10"
    >
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <List />
        </EmptyMedia>
      </EmptyHeader>
      <EmptyTitle>{{ $t('models.emptyTitle') }}</EmptyTitle>
      <EmptyDescription>
        {{ $t(managed ? 'models.managedEmptyDescription' : 'models.emptyDescription') }}
      </EmptyDescription>
    </Empty>
  </SettingsSection>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
  Pagination,
  PaginationContent,
  PaginationEllipsis,
  PaginationFirst,
  PaginationItem,
  PaginationLast,
  PaginationNext,
  PaginationPrevious,
} from '@felinic/ui'
import { Search, List } from 'lucide-vue-next'
import { SettingsSection } from '@felinic/ui'
import CreateModel from '@/components/create-model/index.vue'
import ImportModelsDialog from '@/components/import-models-dialog/index.vue'
import ModelItem from './model-item.vue'
import type { ModelsGetResponse } from '@memohai/sdk'

const PAGE_SIZE = 8

const props = defineProps<{
  providerId: string | undefined
  models: ModelsGetResponse[] | undefined
  deleteModelLoading: boolean
  managed?: boolean
  preview?: boolean
}>()

defineEmits<{
  edit: [model: ModelsGetResponse]
  delete: [id: string]
}>()

const searchQuery = ref('')
const currentPage = ref(1)
// Always offer search once there are models, so the box never disappears for a
// short list (which read as inconsistent between providers). When it's shown,
// model rows align their text to the search placeholder.
const searchVisible = computed(() => !!props.models && props.models.length > 0)

const filteredModels = computed(() => {
  if (!props.models) return []
  const availableModels = props.managed
    ? props.models.filter(model => model.config?.catalog_available !== false)
    : props.models
  if (!searchQuery.value) return availableModels
  const keyword = searchQuery.value.toLowerCase()
  return availableModels.filter((model) => {
    const name = (model.name ?? '').toLowerCase()
    const modelId = (model.model_id ?? '').toLowerCase()
    return name.includes(keyword) || modelId.includes(keyword)
  })
})

const totalPages = computed(() => Math.ceil(filteredModels.value.length / PAGE_SIZE))
const pageStart = computed(() => (currentPage.value - 1) * PAGE_SIZE + 1)
const pageEnd = computed(() => Math.min(currentPage.value * PAGE_SIZE, filteredModels.value.length))
const displayedModels = computed(() => {
  const start = (currentPage.value - 1) * PAGE_SIZE
  return filteredModels.value.slice(start, start + PAGE_SIZE)
})

watch(searchQuery, () => {
  currentPage.value = 1
})
</script>
