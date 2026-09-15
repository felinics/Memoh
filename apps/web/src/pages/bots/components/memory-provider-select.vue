<template>
  <SearchableSelectPopover
    v-model="selected"
    :options="options"
    :placeholder="placeholder || ''"
    :aria-label="placeholder || 'Select memory provider'"
    :search-placeholder="$t('memory.searchPlaceholder')"
    search-aria-label="Search memory providers"
    :empty-text="$t('memory.empty')"
    :show-group-headers="false"
  >
    <template #option-label="{ option }">
      <span
        class="truncate flex-1 text-left"
        :class="{ 'text-muted-foreground': !option.value }"
        :title="option.label"
      >
        {{ option.label }}
      </span>
    </template>
  </SearchableSelectPopover>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import SearchableSelectPopover from '@/components/searchable-select-popover/index.vue'
import type { SearchableSelectOption } from '@/components/searchable-select-popover/index.vue'

interface MemoryProviderItem {
  id?: string
  name?: string
  provider?: string
  config?: Record<string, unknown>
}

const props = defineProps<{
  providers: MemoryProviderItem[]
  placeholder?: string
}>()
const { t } = useI18n()

const selected = defineModel<string>({ default: '' })

const options = computed<SearchableSelectOption[]>(() => {
  const noneOption: SearchableSelectOption = {
    value: '',
    label: t('common.none'),
    keywords: [t('common.none')],
  }
  const providerOptions = props.providers.map((provider) => {
    const memoryMode = typeof provider.config?.memory_mode === 'string' ? provider.config.memory_mode : ''
    return {
      value: provider.id || '',
      label: provider.name || provider.id || '',
      description: provider.provider === 'builtin'
        ? t(`memory.modeNames.${memoryMode || 'graph'}`)
        : provider.provider,
      keywords: [
        provider.name ?? '',
        provider.provider ?? '',
        memoryMode,
      ],
    }
  })
  return [noneOption, ...providerOptions]
})
</script>
