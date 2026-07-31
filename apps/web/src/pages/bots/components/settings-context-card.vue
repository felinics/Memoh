<!-- eslint-disable vue/no-mutating-props -->
<template>
  <SettingsSection :title="$t('bots.settings.blocks.context')">
    <SettingsRow :label="$t('bots.settings.searchProvider')">
      <div class="w-52">
        <SearchProviderSelect
          v-model="form.search_provider_id"
          :providers="searchProviders"
          :placeholder="$t('bots.settings.searchProviderPlaceholder')"
        />
      </div>
    </SettingsRow>

    <SettingsRow :label="$t('bots.settings.fetchProvider')">
      <div class="w-52">
        <FetchProviderSelect
          v-model="form.fetch_provider_id"
          :providers="fetchProviders"
          :placeholder="$t('bots.settings.fetchProviderPlaceholder')"
        />
      </div>
    </SettingsRow>

    <SettingsRow :label="$t('bots.settings.memoryProvider')">
      <div class="w-52">
        <MemoryProviderSelect
          v-model="form.memory_provider_id"
          :providers="memoryProviders"
          :placeholder="$t('bots.settings.memoryProviderPlaceholder')"
        />
      </div>
    </SettingsRow>
  </SettingsSection>
</template>

<script setup lang="ts">
import { SettingsRow, SettingsSection } from '@felinic/ui'
import SearchProviderSelect from './search-provider-select.vue'
import FetchProviderSelect from './fetch-provider-select.vue'
import MemoryProviderSelect from './memory-provider-select.vue'
import type {
  SettingsSettings,
  AdaptersProviderGetResponse,
  FetchprovidersGetResponse,
  SearchprovidersGetResponse,
} from '@memohai/sdk'

defineProps<{
  form: SettingsSettings
  searchProviders: SearchprovidersGetResponse[]
  fetchProviders: FetchprovidersGetResponse[]
  memoryProviders: AdaptersProviderGetResponse[]
}>()
</script>
