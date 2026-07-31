<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useQuery } from '@pinia/colada'
import {
  Button,
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from '@felinic/ui'
import { getModels, getProviders, getProviderTemplates } from '@memohai/sdk'
import type { ModelsGetResponse, ProvidersGetResponse, ProvidertemplatesGetResponse } from '@memohai/sdk'
import { Boxes, Box, ChevronRight, Plus, Search } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { BackendCard, DetailPane, PageShell, SwapTransition } from '@felinic/ui'
import AddProvider from '@/components/add-provider/index.vue'
import ProviderIcon from '@/components/provider-icon/index.vue'
import { useRoutedViewSwap } from '@/composables/useViewSwap'
import { avatarInitials } from '@/composables/useAvatarInitials'
import { isTemplateConfigured, providerDraftFromTemplate, templateDefaultConfig } from '@/utils/provider-template'
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
const searchQuery = ref('')
const addOpen = ref(false)
const initialTemplateId = ref('')

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

const availableTemplates = computed(() => templates.value.filter(template =>
  !isTemplateConfigured(template)
  && template.id !== optimisticProvider.value?.provider_template_id,
))

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

// Always offer search once there's anything to filter — a hidden-then-appearing
// box read as inconsistent (some providers showed it, some didn't).
const showSearch = computed(() => catalogProviders.value.length + availableTemplates.value.length > 0)

const filteredProviders = computed(() => {
  const keyword = searchQuery.value.trim().toLowerCase()
  if (!keyword) return catalogProviders.value
  return catalogProviders.value.filter((p) => {
    const name = (p.name ?? '').toLowerCase()
    const url = providerSubtitle(p).toLowerCase()
    return name.includes(keyword) || url.includes(keyword)
  })
})

const filteredTemplates = computed(() => {
  const keyword = searchQuery.value.trim().toLowerCase()
  if (!keyword) return availableTemplates.value
  return availableTemplates.value.filter(template =>
    (template.name ?? '').toLowerCase().includes(keyword)
    || (template.driver ?? '').toLowerCase().includes(keyword)
    || (template.description ?? '').toLowerCase().includes(keyword),
  )
})

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
  addOpen.value = true
}
</script>

<template>
  <SwapTransition :direction="direction">
    <!-- Gallery -->
    <PageShell
      v-if="view === 'list'"
      :title="t('sidebar.providers')"
    >
      <template #actions>
        <div
          v-if="showSearch"
          class="w-44 sm:w-56"
        >
          <InputGroup class="w-full">
            <InputGroupAddon align="inline-start">
              <Search class="size-3.5 text-muted-foreground" />
            </InputGroupAddon>
            <InputGroupInput
              v-model="searchQuery"
              :placeholder="t('provider.searchPlaceholder')"
            />
          </InputGroup>
        </div>
        <Button @click="openAddProvider()">
          <Plus class="size-4" />
          {{ t('provider.addBtn') }}
        </Button>
      </template>

      <div
        v-if="catalogProviders.length + availableTemplates.length > 0"
        class="grid grid-cols-1 gap-3 sm:grid-cols-2"
      >
        <BackendCard
          v-for="provider in filteredProviders"
          :key="provider.id"
          :name="provider.name ?? ''"
          :subtitle="providerSubtitle(provider)"
          :enabled="provider.enable !== false"
          @click="openProviderInstance(provider)"
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
                {{ avatarInitials(provider.name, '?') }}
              </span>
            </span>
          </template>
          <template #trailing>
            <span
              v-if="modelCount(provider.id) > 0"
              class="flex shrink-0 items-center gap-1 text-xs text-muted-foreground"
            >
              <Boxes class="size-3.5" />
              {{ modelCount(provider.id) }}
            </span>
            <ChevronRight
              v-else
              class="size-4 shrink-0 text-muted-foreground/60"
            />
          </template>
        </BackendCard>

        <BackendCard
          v-for="template in filteredTemplates"
          :key="`template:${template.id}`"
          :name="template.name ?? ''"
          :subtitle="templateSubtitle(template)"
          @click="openTemplate(template)"
        >
          <template #leading>
            <span class="flex size-10 items-center justify-center rounded-full bg-muted">
              <ProviderIcon
                v-if="template.icon"
                :icon="template.icon"
                size="1.5em"
              />
              <span
                v-else
                class="text-xs font-medium text-muted-foreground"
              >
                {{ avatarInitials(template.name, '?') }}
              </span>
            </span>
          </template>
        </BackendCard>
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
        hide-trigger
      />
    </PageShell>

    <!-- Detail -->
    <DetailPane
      v-else
      width="narrow"
      :back-label="t('sidebar.providers')"
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
