<template>
  <!-- Horizontal label|control settings row; stacks on a narrow pane. The label
       keeps a #content slot (not the default :label prop) so the <Label :for>
       association to each field's input survives the migration. -->
  <SettingsRow stack="sm">
    <template #content>
      <Label
        :for="fieldId"
        class="text-sm font-medium text-foreground"
      >
        {{ displayTitle }}
      </Label>
      <p
        v-if="displayDescription"
        class="mt-0.5 text-xs text-muted-foreground"
      >
        {{ displayDescription }}
      </p>
    </template>

    <!-- Fixed control width so every field type lines up down the card. -->
    <div class="w-full sm:w-80">
      <InputGroup v-if="field.type === 'secret'">
        <InputGroupInput
          :id="fieldId"
          :model-value="stringValue"
          :type="revealed ? 'text' : 'password'"
          :placeholder="placeholder"
          @update:model-value="(v: string) => emit('update:modelValue', v)"
        />
        <InputGroupAddon align="inline-end">
          <InputGroupButton
            size="icon-xs"
            variant="quiet"
            :aria-label="revealed
              ? t('bots.channels.hideSecretField', { field: displayTitle })
              : t('bots.channels.showSecretField', { field: displayTitle })"
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
          :model-value="!!modelValue"
          @update:model-value="(v: boolean) => emit('update:modelValue', !!v)"
        />
      </div>

      <Select
        v-else-if="field.type === 'enum' && field.enum"
        :model-value="String(modelValue ?? '')"
        @update:model-value="onEnum"
      >
        <SelectTrigger
          size="sm"
          class="w-full"
        >
          <SelectValue :placeholder="displayTitle" />
        </SelectTrigger>
        <SelectContent class="w-[--reka-select-trigger-width]">
          <SelectItem
            v-for="opt in field.enum"
            :key="opt"
            :value="opt"
          >
            {{ opt }}
          </SelectItem>
        </SelectContent>
      </Select>

      <Input
        v-else-if="field.type === 'number'"
        :id="fieldId"
        :model-value="stringValue"
        type="number"
        :placeholder="placeholder"
        class="w-full tabular-nums"
        @update:model-value="onNumber"
      />

      <Input
        v-else
        :id="fieldId"
        :model-value="stringValue"
        type="text"
        :placeholder="placeholder"
        class="w-full"
        @update:model-value="onText"
      />
    </div>
  </SettingsRow>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Input, Label, Switch,
  Select, SelectTrigger, SelectValue, SelectContent, SelectItem,
  InputGroup, InputGroupInput, InputGroupAddon, InputGroupButton,
} from '@felinic/ui'
import { Eye, EyeOff } from 'lucide-vue-next'
import type { ChannelFieldSchema } from '@memohai/sdk'
import SettingsRow from '@/components/settings/row.vue'

const props = defineProps<{
  field: ChannelFieldSchema
  fieldKey: string
  modelValue: unknown
  title?: string
  description?: string
}>()

const emit = defineEmits<{
  'update:modelValue': [value: unknown]
}>()

const { t } = useI18n()
const revealed = ref(false)

const fieldId = computed(() => `channel-field-${props.fieldKey}`)
const displayTitle = computed(() => props.title?.trim() || props.field.title?.trim() || props.fieldKey)
const displayDescription = computed(() => props.description?.trim() || props.field.description?.trim() || '')
const placeholder = computed(() => (props.field.example != null ? String(props.field.example) : ''))
const stringValue = computed(() => {
  const v = props.modelValue
  return typeof v === 'string' || typeof v === 'number' ? String(v) : ''
})

// Mirror the panel's old number handling: empty clears to '', non-numeric is dropped.
function onNumber(v: string | number) {
  if (v === '') {
    emit('update:modelValue', '')
    return
  }
  const n = typeof v === 'number' ? v : Number(v)
  emit('update:modelValue', Number.isNaN(n) ? '' : n)
}

function onEnum(value: unknown) {
  emit('update:modelValue', typeof value === 'string' ? value : String(value ?? ''))
}

function onText(value: string | number) {
  emit('update:modelValue', String(value))
}
</script>
