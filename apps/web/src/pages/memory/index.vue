<script setup lang="ts">
import { computed, provide, ref } from 'vue'
import { useQuery } from '@pinia/colada'
import { BackendCard, Button, DetailPane, PageShell, SectionGroup, SwapTransition } from '@felinic/ui'
import { getMemoryProviders, getMemoryProvidersMeta } from '@memohai/sdk'
import type { AdaptersProviderGetResponse, AdaptersProviderMeta } from '@memohai/sdk'
import { Brain } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import BuiltinConfig from './components/builtin-config.vue'
import ProviderSetting from './components/provider-setting.vue'
import { useRoutedViewSwap } from '@/composables/useViewSwap'
import { providerConfigDefaults } from '@/utils/provider-template'

const { t } = useI18n()
const MEMORY_PROVIDER_TYPES = ['mem0', 'openviking'] as const

const { data: providerData, isLoading: providersLoading } = useQuery({
  key: () => ['memory-providers'],
  query: async () => {
    const { data } = await getMemoryProviders({ throwOnError: true })
    return data
  },
})

const { data: providerMetaData } = useQuery({
  key: () => ['memory-providers-meta'],
  query: async () => {
    const { data } = await getMemoryProvidersMeta({ throwOnError: true })
    return data
  },
})

const providers = computed<AdaptersProviderGetResponse[]>(() =>
  Array.isArray(providerData.value) ? providerData.value : [],
)
const builtinProvider = computed(() => providers.value.find((p) => p.provider === 'builtin') ?? null)
const providerMetas = computed<AdaptersProviderMeta[]>(() =>
  Array.isArray(providerMetaData.value) ? providerMetaData.value : [],
)
const optimisticProviders = ref<Record<string, AdaptersProviderGetResponse>>({})
const externalProviders = computed<AdaptersProviderGetResponse[]>(() => MEMORY_PROVIDER_TYPES.map((provider) => {
  const meta = providerMetas.value.find(item => item.provider === provider)
  return providers.value.find(instance => instance.provider === provider)
    ?? optimisticProviders.value[provider]
    ?? {
      name: meta?.display_name ?? t(`memory.providerNames.${provider}`, provider),
      provider,
      config: providerConfigDefaults(meta?.config_schema),
    }
}))

// Only the external (advanced) backend opened in the detail pane uses this.
const curProvider = ref<AdaptersProviderGetResponse | null>(null)
provide('curMemoryProvider', curProvider)

// The built-in config owns the model draft + save; the Save button lives in
// this page's header (#actions), so read its state off the child instead of
// hoisting all the memory logic up here.
const builtinRef = ref<InstanceType<typeof BuiltinConfig> | null>(null)

// Page-owned query key (unique under settings KeepAlive — see useViewSwap.ts).
const {
  view,
  direction,
  isDetailLoading,
  openDetail: openExternal,
  backToList: closeExternal,
} = useRoutedViewSwap({
  key: 'memoryBackend',
  items: () => externalProviders.value,
  selected: () => curProvider.value ?? undefined,
  select: provider => curProvider.value = provider ?? null,
  getRouteValue: provider => provider.provider ?? '',
  isLoading: () => providersLoading.value,
  isReady: () => providerData.value !== undefined,
})

function handleMaterialized(provider: AdaptersProviderGetResponse) {
  if (!provider.provider) return
  optimisticProviders.value = {
    ...optimisticProviders.value,
    [provider.provider]: provider,
  }
  curProvider.value = provider
}

// BackendCard subtitle: the provider TYPE name, only when it adds information —
// an unconfigured draft's display name IS the type name, and showing it twice
// reads as a stutter ("Mem0 / Mem0").
function providerSubtitle(provider: AdaptersProviderGetResponse): string {
  const typeName = t(`memory.providerNames.${provider.provider}`, provider.provider ?? '')
  return provider.name && provider.name !== typeName ? typeName : ''
}
</script>

<template>
  <SwapTransition :direction="direction">
    <!-- Capability config -->
    <PageShell
      v-if="view === 'list'"
      :title="t('sidebar.memory')"
    >
      <!-- Root-page manual save: picking an embedding model provisions an index
           backend, so it batches behind one deliberate Save rather than
           auto-saving. It lives in the header (disabled while synced) — the
           house pattern for a PageShell page — not a footer band inside the
           card. -->
      <template #actions>
        <Button
          :disabled="!builtinRef?.hasChanges || builtinRef?.saveLoading"
          :loading="builtinRef?.saveLoading"
          @click="builtinRef?.save()"
        >
          {{ t('common.saveChanges') }}
        </Button>
      </template>

      <div class="space-y-8">
        <BuiltinConfig
          ref="builtinRef"
          :provider="builtinProvider"
        />

        <!-- External backends: a rare concern, listed plainly below the built-in
             group rather than hidden behind a disclosure — two quiet cards do
             not need a collapse, and the hand-rolled one fought the section
             rhythm. -->
        <SectionGroup
          :title="t('memory.advanced')"
          :description="t('memory.advancedHint')"
        >
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <BackendCard
              v-for="provider in externalProviders"
              :key="provider.provider"
              :name="provider.name ?? ''"
              :subtitle="providerSubtitle(provider)"
              @click="openExternal(provider)"
            >
              <template #leading>
                <span class="flex size-10 items-center justify-center rounded-full bg-muted">
                  <Brain class="size-5 text-muted-foreground" />
                </span>
              </template>
            </BackendCard>
          </div>
        </SectionGroup>
      </div>
    </PageShell>

    <!-- External backend detail -->
    <DetailPane
      v-else
      width="narrow"
      :back-label="t('sidebar.memory')"
      :loading="isDetailLoading || !curProvider"
      @back="closeExternal"
    >
      <ProviderSetting
        v-if="curProvider"
        @materialized="handleMaterialized"
      />
    </DetailPane>
  </SwapTransition>
</template>
