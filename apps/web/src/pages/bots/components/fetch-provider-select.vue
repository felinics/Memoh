<template>
  <SearchableSelectPopover
    v-model="selected"
    :options="options"
    :placeholder="placeholder || ''"
    :aria-label="placeholder || 'Select fetch provider'"
    :search-placeholder="$t('webSearch.searchPlaceholder')"
    search-aria-label="Search fetch providers"
    :empty-text="$t('webSearch.emptyFetch')"
    :show-group-headers="false"
  >
    <template #trigger="{ open, displayLabel }">
      <button
        data-slot="select-trigger"
        data-size="default"
        :data-placeholder="!selected ? '' : undefined"
        type="button"
        :aria-expanded="open"
        :aria-label="placeholder || 'Select fetch provider'"
        :class="[selectTriggerClass, 'w-full']"
      >
        <span class="line-clamp-1">{{ displayLabel || selectedProvider?.name || placeholder }}</span>
        <ChevronsUpDown class="opacity-50" />
      </button>
    </template>

    <template #option-label="{ option }">
      <span
        class="truncate flex-1 text-left"
        :title="option.label"
      >
        {{ option.label }}
      </span>
    </template>
  </SearchableSelectPopover>
</template>

<script setup lang="ts">
import { ChevronsUpDown } from 'lucide-vue-next'
import { selectTriggerClass } from '@felinic/ui'
import { computed } from 'vue'
import type { FetchprovidersGetResponse } from '@memohai/sdk'
import { useI18n } from 'vue-i18n'
import SearchableSelectPopover from '@/components/searchable-select-popover/index.vue'
import type { SearchableSelectOption } from '@/components/searchable-select-popover/index.vue'

const props = defineProps<{
  providers: FetchprovidersGetResponse[]
  placeholder?: string
}>()
const { t } = useI18n()

const selected = defineModel<string>({ default: '' })

const nativeProvider = computed(() => props.providers.find((p) => p.provider === 'native'))

const selectedProvider = computed(() => {
  if (!selected.value) return nativeProvider.value
  return props.providers.find((p) => p.id === selected.value) ?? nativeProvider.value
})

const options = computed<SearchableSelectOption[]>(() => {
  return props.providers.map((provider) => ({
    value: provider.id || '',
    label: provider.name || provider.id || '',
    description: provider.enable === false ? t('common.disabled') : provider.provider,
    keywords: [provider.name ?? '', provider.provider ?? '', provider.enable === false ? t('common.disabled') : '', t(`webSearch.fetchProviderNames.${provider.provider}`, '')],
  }))
})
</script>
