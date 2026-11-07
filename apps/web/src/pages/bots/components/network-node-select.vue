<template>
  <SearchableSelectPopover
    v-model="selected"
    :options="options"
    :placeholder="placeholder || ''"
    :aria-label="placeholder || 'Select exit node'"
    :search-placeholder="$t('network.searchPlaceholder')"
    search-aria-label="Search exit nodes"
    :empty-text="$t('network.empty')"
    :show-group-headers="false"
  >
    <template #option-label="{ option }">
      <span
        class="flex-1 truncate text-left"
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
import type { NetworkNodeOption } from '@memohai/sdk'

const props = defineProps<{
  nodes: NetworkNodeOption[]
  placeholder?: string
}>()

const selected = defineModel<string>({ default: '' })
const { t } = useI18n()

const options = computed<SearchableSelectOption[]>(() => {
  const noneOption: SearchableSelectOption = {
    value: '',
    label: t('common.none'),
    keywords: [t('common.none')],
  }
  const nodeOptions = props.nodes.map(node => {
    const addresses = node.addresses?.join(' ') ?? ''
    return {
      value: node.value || '',
      label: node.display_name || node.value || '',
      description: [node.description, addresses].filter(Boolean).join(' | '),
      keywords: [
        node.display_name ?? '',
        node.value ?? '',
        node.description ?? '',
        addresses,
      ],
      meta: {
        online: node.online ?? false,
      },
    } satisfies SearchableSelectOption
  })
  return [noneOption, ...nodeOptions]
})
</script>
