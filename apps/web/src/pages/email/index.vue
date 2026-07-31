<script setup lang="ts">
import { computed, provide, ref, watch } from 'vue'
import { useQuery } from '@pinia/colada'
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from '@felinic/ui'
import { getEmailProviders, getEmailProvidersMeta } from '@memohai/sdk'
import type { EmailProviderMeta, EmailProviderResponse } from '@memohai/sdk'
import { Search } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { BackendCard, DetailPane, PageShell, SwapTransition } from '@felinic/ui'
import AddEmailProvider from './components/add-email-provider.vue'
import ProviderSetting from './components/provider-setting.vue'
import { useRoutedViewSwap } from '@/composables/useViewSwap'
import EmailProviderIcon from '@/components/email-provider-icon/index.vue'
import { providerConfigDefaults } from '@/utils/provider-template'

const { t } = useI18n()
const EMAIL_PROVIDER_TYPES = ['generic', 'gmail', 'mailgun'] as const

const { data: providerData, isLoading: providersLoading } = useQuery({
  key: () => ['email-providers'],
  query: async () => {
    const { data } = await getEmailProviders({ throwOnError: true })
    return data
  },
})

const { data: providerMetaData } = useQuery({
  key: () => ['email-providers-meta'],
  query: async () => {
    const { data } = await getEmailProvidersMeta({ throwOnError: true })
    return data
  },
})

const curProvider = ref<EmailProviderResponse>()
const optimisticProviders = ref<EmailProviderResponse[]>([])
provide('curEmailProvider', curProvider)

const searchQuery = ref('')
const addOpen = ref(false)

const providers = computed<EmailProviderResponse[]>(() =>
  Array.isArray(providerData.value) ? providerData.value : [],
)
const providerMetas = computed<EmailProviderMeta[]>(() =>
  Array.isArray(providerMetaData.value) ? providerMetaData.value : [],
)

const materializedProviders = computed<EmailProviderResponse[]>(() => [
  ...optimisticProviders.value.filter(optimistic =>
    !providers.value.some(provider => provider.id === optimistic.id),
  ),
  ...providers.value,
])

watch(providers, (items) => {
  optimisticProviders.value = optimisticProviders.value.filter(optimistic =>
    !items.some(provider => provider.id === optimistic.id),
  )
})

const providerItems = computed<EmailProviderResponse[]>(() => {
  const fixedItems = EMAIL_PROVIDER_TYPES.flatMap((provider) => {
    const instances = materializedProviders.value.filter(instance => instance.provider === provider)
    if (instances.length > 0) return instances

    const meta = providerMetas.value.find(item => item.provider === provider)
    return [{
      name: meta?.display_name ?? provider,
      provider,
      config: providerConfigDefaults(meta?.config_schema),
    }]
  })
  const otherItems = materializedProviders.value.filter(instance =>
    !EMAIL_PROVIDER_TYPES.includes(instance.provider as typeof EMAIL_PROVIDER_TYPES[number]),
  )
  return [...fixedItems, ...otherItems]
})

function providerRouteValue(provider: EmailProviderResponse) {
  if (provider.id) return provider.id
  return provider.provider ? `template:${provider.provider}` : ''
}

// Page-owned query key (unique under settings KeepAlive — see useViewSwap.ts).
const {
  view,
  direction,
  isDetailLoading,
  openDetail: openProvider,
  backToList: closeProvider,
} = useRoutedViewSwap({
  key: 'emailProvider',
  items: () => providerItems.value,
  selected: () => curProvider.value,
  select: provider => curProvider.value = provider,
  getRouteValue: providerRouteValue,
  isLoading: () => providersLoading.value,
  isReady: () => providerData.value !== undefined,
})

const showSearch = computed(() => providerItems.value.length > 0)

const filteredProviders = computed(() => {
  const keyword = searchQuery.value.trim().toLowerCase()
  if (!keyword) return providerItems.value
  return providerItems.value.filter(p =>
    (p.name ?? '').toLowerCase().includes(keyword)
    || (p.provider ?? '').toLowerCase().includes(keyword),
  )
})

function handleMaterialized(provider: EmailProviderResponse) {
  if (provider.id && !optimisticProviders.value.some(item => item.id === provider.id)) {
    optimisticProviders.value = [provider, ...optimisticProviders.value]
  }
  openProvider(provider)
}
</script>

<template>
  <SwapTransition :direction="direction">
    <!-- Provider list -->
    <PageShell
      v-if="view === 'list'"
      :title="t('email.title')"
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
              :placeholder="t('email.searchPlaceholder')"
            />
          </InputGroup>
        </div>
        <AddEmailProvider v-model:open="addOpen" />
      </template>

      <div
        v-if="providerItems.length > 0"
        class="grid grid-cols-1 gap-3 sm:grid-cols-2"
      >
        <BackendCard
          v-for="provider in filteredProviders"
          :key="providerRouteValue(provider)"
          :name="provider.name ?? ''"
          @click="openProvider(provider)"
        >
          <template #leading>
            <span class="flex size-10 items-center justify-center rounded-full bg-muted">
              <EmailProviderIcon
                :provider="provider.provider"
                class="size-5 text-muted-foreground"
              />
            </span>
          </template>
        </BackendCard>
      </div>
    </PageShell>

    <!-- Provider detail -->
    <DetailPane
      v-else
      width="narrow"
      :back-label="t('email.title')"
      :loading="isDetailLoading || !curProvider"
      @back="closeProvider"
    >
      <ProviderSetting
        v-if="curProvider"
        @materialized="handleMaterialized"
      />
    </DetailPane>
  </SwapTransition>
</template>
