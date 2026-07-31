<template>
  <div class="space-y-4">
    <FieldContent
      v-for="schemaField in visibleFields"
      :key="schemaField.key"
      :field="schemaField"
      :id-prefix="idPrefix"
      :disabled="disabled"
    />

    <div
      v-if="collapsedFields.length"
      class="space-y-4"
    >
      <button
        type="button"
        class="flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors"
        @click="showCollapsed = !showCollapsed"
      >
        <component
          :is="showCollapsed ? ChevronDown : ChevronRight"
          class="size-3.5"
        />
        {{ t('common.advanced') }}
      </button>

      <template v-if="showCollapsed">
        <FieldContent
          v-for="schemaField in collapsedFields"
          :key="schemaField.key"
          :field="schemaField"
          :id-prefix="idPrefix"
          :disabled="disabled"
        />
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import {
  Input,
  Label,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Switch,
  Textarea,
} from '@felinic/ui'
import { computed, defineComponent, h, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronDown, ChevronRight, Eye, EyeOff } from 'lucide-vue-next'
import { FieldStack } from '@felinic/ui'
import type { ConfigSchema, ConfigSchemaField } from './types'
import { cloneConfig, getPathValue, setPathValue } from './utils'

const props = withDefaults(defineProps<{
  schema?: ConfigSchema | null
  modelValue: Record<string, unknown>
  idPrefix?: string
  disabled?: boolean
}>(), {
  schema: undefined,
  idPrefix: 'config-field',
  disabled: false,
})

const emit = defineEmits<{
  'update:modelValue': [value: Record<string, unknown>]
}>()

const { t } = useI18n()
const visibleSecrets = reactive<Record<string, boolean>>({})
const showCollapsed = ref(false)

const orderedFields = computed(() =>
  [...(props.schema?.fields ?? [])].sort((a, b) => (a.order ?? 0) - (b.order ?? 0)),
)

const visibleFields = computed(() => orderedFields.value.filter(f => !f.collapsed))
const collapsedFields = computed(() => orderedFields.value.filter(f => f.collapsed))

function getValue(path: string) {
  const current = getPathValue(props.modelValue, path)
  if (current !== undefined) return current
  const field = orderedFields.value.find(item => item.key === path)
  return field?.default
}

function stringValue(path: string) {
  const value = getValue(path)
  return typeof value === 'string' ? value : value == null ? '' : String(value)
}

function numberValue(path: string) {
  const value = getValue(path)
  return typeof value === 'number' ? String(value) : value == null ? '' : String(value)
}

function placeholderOf(field: ConfigSchemaField) {
  return field.placeholder || (field.example != null ? String(field.example) : '')
}

function updateValue(path: string, value: unknown) {
  const next = cloneConfig(props.modelValue)
  setPathValue(next, path, value)
  emit('update:modelValue', next)
}

function updateNumber(path: string, value: string) {
  const nextValue = value === '' ? undefined : Number(value)
  updateValue(path, nextValue)
}

// Renders a single field — extracted into a child component so the template
// is written once and reused for both visible and collapsed sections.
const FieldContent = defineComponent({
  props: {
    field: { type: Object as () => ConfigSchemaField, required: true },
    idPrefix: { type: String, required: true },
    disabled: { type: Boolean, default: false },
  },
  setup(fieldProps) {
    return () => {
      const field = fieldProps.field
      const fieldId = `${fieldProps.idPrefix}-${field.key}`
      const isLabelFor = field.type !== 'bool' && field.type !== 'enum' ? fieldId : undefined

      const controlChildren: ReturnType<typeof h>[] = []

      // Input
      if (field.type === 'secret') {
        const isVisible = visibleSecrets[field.key]
        controlChildren.push(
          h('div', { class: 'relative' }, [
            h(Input, {
              id: fieldId,
              modelValue: stringValue(field.key),
              type: isVisible ? 'text' : 'password',
              placeholder: placeholderOf(field),
              disabled: fieldProps.disabled,
              readonly: field.readonly,
              'onUpdate:modelValue': (val: string) => updateValue(field.key, val),
            }),
            h('button', {
              type: 'button',
              class: 'absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground',
              disabled: fieldProps.disabled,
              onClick: () => { visibleSecrets[field.key] = !visibleSecrets[field.key] },
            }, [h(isVisible ? EyeOff : Eye, { class: 'size-3.5' })]),
          ]),
        )
      } else if (field.type === 'bool') {
        controlChildren.push(
          h(Switch, {
            modelValue: !!getValue(field.key),
            disabled: fieldProps.disabled || field.readonly,
            'onUpdate:modelValue': (val: boolean) => updateValue(field.key, !!val),
          }),
        )
      } else if (field.type === 'number') {
        controlChildren.push(
          h(Input, {
            id: fieldId,
            modelValue: numberValue(field.key),
            type: 'number',
            placeholder: placeholderOf(field),
            disabled: fieldProps.disabled,
            readonly: field.readonly,
            min: field.constraint?.min,
            max: field.constraint?.max,
            step: field.constraint?.step ?? 1,
            'onUpdate:modelValue': (val: string) => updateNumber(field.key, val),
          }),
        )
      } else if (field.type === 'enum' && field.enum) {
        controlChildren.push(
          h(Select, {
            modelValue: stringValue(field.key),
            disabled: fieldProps.disabled || field.readonly,
            'onUpdate:modelValue': (val: string) => updateValue(field.key, val),
          }, () => [
            h(SelectTrigger, null, () => [
              h(SelectValue, { placeholder: field.title || field.key }),
            ]),
            h(SelectContent, null, () =>
              (field.enum ?? []).map(option =>
                h(SelectItem, { key: option, value: option }, () => option),
              ),
            ),
          ]),
        )
      } else if (field.type === 'textarea' || field.multiline) {
        controlChildren.push(
          h(Textarea, {
            id: fieldId,
            modelValue: stringValue(field.key),
            placeholder: placeholderOf(field),
            disabled: fieldProps.disabled,
            readonly: field.readonly,
            rows: 4,
            'onUpdate:modelValue': (val: string) => updateValue(field.key, val),
          }),
        )
      } else {
        controlChildren.push(
          h(Input, {
            id: fieldId,
            modelValue: stringValue(field.key),
            type: 'text',
            placeholder: placeholderOf(field),
            disabled: fieldProps.disabled,
            readonly: field.readonly,
            'onUpdate:modelValue': (val: string) => updateValue(field.key, val),
          }),
        )
      }

      return h(FieldStack, {
        for: isLabelFor,
        help: field.description || undefined,
      }, {
        label: () => h(Label, { for: isLabelFor }, () => [
          field.title || field.key,
          !field.required
            ? h('span', { class: 'text-xs text-muted-foreground ml-1' }, `(${t('common.optional')})`)
            : null,
        ]),
        default: () => controlChildren,
      })
    }
  },
})
</script>
