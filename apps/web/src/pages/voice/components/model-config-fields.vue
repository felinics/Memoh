<template>
  <div class="grid gap-4 md:grid-cols-2">
    <FieldStack
      v-for="field in fields"
      :key="field.key"
      :label="field.title || field.key"
      :for="field.type === 'bool' || field.type === 'enum' ? undefined : `${idPrefix}-${field.key}`"
      :help="field.description"
      :class="isWideField(field) ? 'md:col-span-2' : ''"
    >
      <InputGroup v-if="field.type === 'secret'">
        <InputGroupInput
          :id="`${idPrefix}-${field.key}`"
          :model-value="stringValue(field.key)"
          :type="visibleSecrets[field.key] ? 'text' : 'password'"
          :placeholder="fieldPlaceholder(field)"
          @update:model-value="(val: string) => emit('update:field', field.key, val)"
        />
        <InputGroupAddon align="inline-end">
          <InputGroupButton
            type="button"
            size="icon-xs"
            variant="quiet"
            :aria-label="visibleSecrets[field.key]
              ? t('common.hideSecretField', { field: field.title || field.key })
              : t('common.showSecretField', { field: field.title || field.key })"
            @click="visibleSecrets[field.key] = !visibleSecrets[field.key]"
          >
            <component :is="visibleSecrets[field.key] ? EyeOff : Eye" />
          </InputGroupButton>
        </InputGroupAddon>
      </InputGroup>

      <Switch
        v-else-if="field.type === 'bool'"
        :model-value="!!values[field.key]"
        @update:model-value="(val: boolean) => emit('update:field', field.key, !!val)"
      />

      <NumberField
        v-else-if="field.type === 'number'"
        :id="`${idPrefix}-${field.key}`"
        :model-value="numberValue(field.key)"
        :placeholder="fieldPlaceholder(field)"
        disable-wheel-change
        @update:model-value="(val) => emit('update:field', field.key, val)"
      />

      <SearchableSelectPopover
        v-else-if="field.type === 'enum' && field.enum"
        :model-value="stringValue(field.key)"
        :options="enumOptions(field)"
        :placeholder="enumPlaceholder(field)"
        :aria-label="field.title || field.key"
        :search-placeholder="t('common.search')"
        :search-aria-label="t('common.search')"
        :empty-text="t('common.noData')"
        :show-group-headers="false"
        popover-class="w-[var(--reka-popover-trigger-width)]"
        @update:model-value="(val) => emit('update:field', field.key, val)"
      />

      <Input
        v-else
        :id="`${idPrefix}-${field.key}`"
        :model-value="stringValue(field.key)"
        type="text"
        :placeholder="fieldPlaceholder(field)"
        @update:model-value="(val) => emit('update:field', field.key, val)"
      />
    </FieldStack>
  </div>
</template>

<script setup lang="ts">
import { reactive } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  FieldStack,
  Input,
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupInput,
  NumberField,
  Switch,
} from '@felinic/ui'
import type { AudioFieldSchema } from '@memohai/sdk'
import { Eye, EyeOff } from 'lucide-vue-next'
import SearchableSelectPopover, { type SearchableSelectOption } from '@/components/searchable-select-popover/index.vue'

export type AudioFieldWithKey = AudioFieldSchema & { key: string }

const props = withDefaults(defineProps<{
  fields: AudioFieldWithKey[]
  values: Record<string, unknown>
  idPrefix?: string
}>(), {
  idPrefix: 'tts-field',
})

const emit = defineEmits<{
  'update:field': [key: string, value: unknown]
}>()

const { t } = useI18n()
const visibleSecrets = reactive<Record<string, boolean>>({})

function isWideField(field: AudioFieldWithKey) {
  if (field.type === 'secret') return true
  const key = field.key.toLowerCase()
  if (key.includes('url') || key.includes('endpoint') || key.includes('key') || key.includes('token') || key.includes('path') || key.includes('uri')) return true
  return (field.description ?? '').length > 80
}

function fieldPlaceholder(field: AudioFieldWithKey) {
  return field.example != null ? String(field.example) : ''
}

function stringValue(key: string) {
  const value = props.values[key]
  return value == null ? '' : String(value)
}

function numberValue(key: string) {
  const value = props.values[key]
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function enumOptions(field: AudioFieldWithKey): SearchableSelectOption[] {
  return (field.enum ?? []).map(option => ({ value: option, label: option }))
}

function enumPlaceholder(field: AudioFieldWithKey) {
  if (field.key === 'voice') return t('speech.fields.voicePlaceholder')
  if (field.key === 'format') return t('speech.fields.formatPlaceholder')
  return field.title || field.key
}
</script>
