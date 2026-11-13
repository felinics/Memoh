<template>
  <!-- Horizontal label|control settings row; stacks on a narrow pane, and
       always for a multi-line field. The label keeps a #content slot (not the
       default :label prop) so the <Label :for> association to each field's
       input survives. -->
  <SettingsRow :stack="fullWidth ? 'always' : 'sm'">
    <template #content>
      <Label
        :for="isKeyValueField ? undefined : fieldId"
        class="text-sm font-medium text-foreground"
      >
        {{ label }}
      </Label>
      <p
        v-if="field.description"
        class="mt-0.5 text-xs text-muted-foreground"
      >
        {{ field.description }}
      </p>
    </template>

    <!-- Fixed control width so every field type lines up down the card. -->
    <div :class="fullWidth ? 'w-full' : 'w-full sm:w-80'">
      <KeyValueEditor
        v-if="isKeyValueField"
        :model-value="headerRows"
        :readonly="field.readonly"
        :value-type="fieldKey === 'headers' ? 'password' : 'text'"
        :key-placeholder="fieldKey === 'headers' ? t('webSearch.headerName') : undefined"
        :value-placeholder="fieldKey === 'headers' ? t('webSearch.headerValue') : undefined"
        :key-aria-label="fieldKey === 'headers' ? t('webSearch.headerName') : label"
        :value-aria-label="fieldKey === 'headers' ? t('webSearch.headerValue') : label"
        :add-label="fieldKey === 'headers' ? t('webSearch.addHeader') : undefined"
        @update:model-value="handleKeyValueChange"
      />

      <Textarea
        v-else-if="multiline"
        :id="fieldId"
        :model-value="stringValue"
        :placeholder="placeholder"
        :readonly="field.readonly"
        rows="4"
        class="min-h-24 resize-none"
        @update:model-value="(v) => emit('update:modelValue', v)"
      />

      <InputGroup v-else-if="field.type === 'secret'">
        <InputGroupInput
          :id="fieldId"
          :model-value="stringValue"
          :type="revealed ? 'text' : 'password'"
          :placeholder="placeholder"
          :readonly="field.readonly"
          @update:model-value="(v: string) => emit('update:modelValue', v)"
        />
        <InputGroupAddon align="inline-end">
          <InputGroupButton
            size="icon-xs"
            variant="quiet"
            :aria-label="revealed
              ? t('common.hideSecretField', { field: label })
              : t('common.showSecretField', { field: label })"
            @click="revealed = !revealed"
          >
            <component :is="revealed ? EyeOff : Eye" />
          </InputGroupButton>
        </InputGroupAddon>
      </InputGroup>

      <div
        v-else-if="field.type === 'bool'"
        class="flex sm:justify-end"
      >
        <Switch
          :model-value="boolValue"
          :disabled="field.readonly"
          @update:model-value="(v: boolean) => emit('update:modelValue', !!v)"
        />
      </div>

      <Select
        v-else-if="(field.type === 'enum' || field.enum?.length) && field.enum?.length"
        :model-value="String(modelValue ?? '')"
        :disabled="field.readonly"
        @update:model-value="(v) => emit('update:modelValue', v)"
      >
        <SelectTrigger
          class="w-full"
        >
          <SelectValue :placeholder="label" />
        </SelectTrigger>
        <SelectContent class="w-(--reka-select-trigger-width)">
          <SelectItem
            v-for="opt in field.enum"
            :key="opt"
            :value="opt"
          >
            {{ opt }}
          </SelectItem>
        </SelectContent>
      </Select>

      <NumberField
        v-else-if="field.type === 'number'"
        :id="fieldId"
        :model-value="numberValue"
        :placeholder="placeholder"
        :readonly="field.readonly"
        :min="field.constraint?.min"
        :max="field.constraint?.max"
        :step="field.constraint?.step ?? 1"
        :format-options="{ useGrouping: false }"
        class="w-full"
        disable-wheel-change
        @update:model-value="(v: number | undefined) => emit('update:modelValue', v)"
      />

      <Input
        v-else
        :id="fieldId"
        :model-value="stringValue"
        type="text"
        :placeholder="placeholder"
        :readonly="field.readonly"
        class="w-full"
        @update:model-value="(v) => emit('update:modelValue', v)"
      />
    </div>
  </SettingsRow>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Input, Label, NumberField, Switch, Textarea,
  Select, SelectTrigger, SelectValue, SelectContent, SelectItem,
  InputGroup, InputGroupInput, InputGroupAddon, InputGroupButton,
  SettingsRow,
} from '@felinic/ui'
import { Eye, EyeOff } from 'lucide-vue-next'
import KeyValueEditor, { type KeyValuePair } from '@/components/key-value-editor/index.vue'

// The parts of a server-declared config field the control needs. Channel and
// network schemas both satisfy it structurally.
export interface SchemaFieldSpec {
  type?: string
  title?: string
  description?: string
  placeholder?: string
  example?: unknown
  enum?: string[]
  multiline?: boolean
  readonly?: boolean
  constraint?: { min?: number; max?: number; step?: number } | null
}

const props = defineProps<{
  field: SchemaFieldSpec
  fieldKey: string
  idPrefix: string
  modelValue: unknown
}>()

// A cleared number field emits undefined; every other control emits its value.
const emit = defineEmits<{
  'update:modelValue': [value: unknown]
}>()

const { t } = useI18n()
const revealed = ref(false)
const headerRows = ref<KeyValuePair[]>([])

const fieldId = computed(() => `${props.idPrefix}-${props.fieldKey}`)
const label = computed(() => props.field.title || props.fieldKey)
const multiline = computed(() => props.field.type === 'textarea' || !!props.field.multiline)
const isKeyValueField = computed(() => props.fieldKey === 'headers' || props.field.type === 'object')
const fullWidth = computed(() => multiline.value || isKeyValueField.value)
const placeholder = computed(() =>
  props.field.placeholder || (props.field.example != null ? String(props.field.example) : ''),
)
const stringValue = computed(() => {
  const v = props.modelValue
  return typeof v === 'string' || typeof v === 'number' ? String(v) : ''
})
const numberValue = computed(() => (typeof props.modelValue === 'number' ? props.modelValue : undefined))
const boolValue = computed(() =>
  props.modelValue === undefined || props.modelValue === null
    ? props.field.example === true
    : Boolean(props.modelValue),
)

function pairsToRecord(pairs: KeyValuePair[]): Record<string, string> {
  return Object.fromEntries(pairs
    .filter(pair => pair.key.trim())
    .map(pair => [pair.key.trim(), pair.value]))
}

function recordToPairs(value: unknown): KeyValuePair[] | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null
  return Object.entries(value as Record<string, unknown>)
    .map(([key, item]) => ({ key, value: String(item ?? '') }))
}

watch(
  () => props.modelValue,
  (val, prev) => {
    if (!isKeyValueField.value) return
    const incoming = recordToPairs(val)
    // Only replace local rows when serialized non-empty entries actually differ,
    // so rows being typed (including newly added blank rows) survive v-model echo.
    if (incoming === null) {
      if (prev !== undefined || Object.keys(pairsToRecord(headerRows.value)).length > 0) {
        headerRows.value = []
      }
      return
    }
    if (JSON.stringify(pairsToRecord(incoming)) !== JSON.stringify(pairsToRecord(headerRows.value))) {
      headerRows.value = incoming
    }
  },
  { immediate: true, deep: true },
)

function handleKeyValueChange(nextRows: KeyValuePair[]) {
  headerRows.value = nextRows
  emit('update:modelValue', pairsToRecord(nextRows))
}
</script>
