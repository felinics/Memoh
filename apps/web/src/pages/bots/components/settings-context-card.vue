<!-- eslint-disable vue/no-mutating-props -->
<template>
  <SettingsSection :title="$t('bots.settings.blocks.context')">
    <SettingsRow
      :label="$t('bots.settings.searchProvider')"
      stack="sm"
    >
      <div class="flex w-full justify-end sm:w-52">
        <SearchProviderSelect
          v-if="searchProviders.length > 0 || form.search_provider_id"
          v-model="form.search_provider_id"
          popover-align="end"
          :providers="searchProviders"
          :placeholder="$t('bots.settings.searchProviderPlaceholder')"
        />
        <!-- Same rule as Multimedia: a select with nothing real to pick is a
             dead end — swap it for a doorway to the page that adds one. -->
        <Button
          v-else
          variant="outline"
          size="sm"
          @click="openProviderSettings('web-search')"
        >
          <Plus />
          {{ $t('provider.add') }}
        </Button>
      </div>
    </SettingsRow>

    <SettingsRow
      :label="$t('bots.settings.fetchProvider')"
      stack="sm"
    >
      <div class="flex w-full justify-end sm:w-52">
        <FetchProviderSelect
          v-if="hasExternalFetchProviders || form.fetch_provider_id"
          v-model="form.fetch_provider_id"
          popover-align="end"
          :providers="fetchProviders"
          :placeholder="$t('bots.settings.fetchProviderPlaceholder')"
        />
        <!-- The built-in native fetcher needs no configuration, so it doesn't
             count as an option; with only native present the select has
             nothing to decide. -->
        <Button
          v-else
          variant="outline"
          size="sm"
          @click="openProviderSettings('web-search')"
        >
          <Plus />
          {{ $t('provider.add') }}
        </Button>
      </div>
    </SettingsRow>

    <SettingsRow
      :label="$t('bots.settings.memoryProvider')"
      stack="sm"
    >
      <div class="flex w-full justify-end sm:w-52">
        <MemoryProviderSelect
          v-if="memoryProviders.length > 0 || form.memory_provider_id"
          v-model="form.memory_provider_id"
          popover-align="end"
          :providers="memoryProviders"
          :placeholder="$t('bots.settings.memoryProviderPlaceholder')"
        />
        <Button
          v-else
          variant="outline"
          size="sm"
          @click="openProviderSettings('memory')"
        >
          <Plus />
          {{ $t('provider.add') }}
        </Button>
      </div>
    </SettingsRow>
  </SettingsSection>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { Plus } from 'lucide-vue-next'
import { Button, SettingsRow, SettingsSection } from '@felinic/ui'
import SearchProviderSelect from './search-provider-select.vue'
import FetchProviderSelect from './fetch-provider-select.vue'
import MemoryProviderSelect from './memory-provider-select.vue'
import type {
  SettingsSettings,
  AdaptersProviderGetResponse,
  FetchprovidersGetResponse,
  SearchprovidersGetResponse,
} from '@memohai/sdk'

const props = defineProps<{
  form: SettingsSettings
  searchProviders: SearchprovidersGetResponse[]
  fetchProviders: FetchprovidersGetResponse[]
  memoryProviders: AdaptersProviderGetResponse[]
}>()

const router = useRouter()

const hasExternalFetchProviders = computed(() =>
  props.fetchProviders.some((p) => p.provider !== 'native'),
)

function openProviderSettings(routeName: 'web-search' | 'memory'): void {
  void router.push({ name: routeName })
}
</script>
