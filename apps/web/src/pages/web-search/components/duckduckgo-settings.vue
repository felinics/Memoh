<template>
  <SettingsRow
    label="Base URL"
    stack="sm"
  >
    <Input
      id="duckduckgo-base-url"
      v-model="localConfig.base_url"
      class="w-full sm:w-80"
      aria-label="Base URL"
    />
  </SettingsRow>
  <SettingsRow
    label="Timeout (seconds)"
    stack="sm"
  >
    <NumberField
      id="duckduckgo-timeout-seconds"
      :model-value="localConfig.timeout_seconds"
      :min="1"
      class="w-full sm:w-80"
      aria-label="Timeout (seconds)"
      disable-wheel-change
      @update:model-value="(value) => localConfig.timeout_seconds = value"
    />
  </SettingsRow>
</template>

<script setup lang="ts">
import { reactive, watch } from 'vue'
import { Input, NumberField, SettingsRow } from '@felinic/ui'

const props = defineProps<{
  modelValue: Record<string, unknown>
}>()

const emit = defineEmits<{
  'update:modelValue': [value: Record<string, unknown>]
}>()

const localConfig = reactive({
  base_url: 'https://html.duckduckgo.com/html/',
  // NumberField commits undefined for an emptied field; the key drops out of
  // the emitted config and hydration restores the default on the next load.
  timeout_seconds: 15 as number | undefined,
})

watch(
  () => props.modelValue,
  (val) => {
    localConfig.base_url = String(val?.base_url ?? 'https://html.duckduckgo.com/html/')
    const timeout = Number(val?.timeout_seconds ?? 15)
    localConfig.timeout_seconds = Number.isFinite(timeout) && timeout > 0 ? timeout : 15
  },
  { immediate: true, deep: true },
)

watch(localConfig, () => {
  emit('update:modelValue', {
    base_url: localConfig.base_url,
    timeout_seconds: localConfig.timeout_seconds,
  })
}, { deep: true })
</script>
