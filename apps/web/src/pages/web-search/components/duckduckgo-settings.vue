<template>
  <SettingsRow label="Base URL">
    <Input
      id="duckduckgo-base-url"
      v-model="localConfig.base_url"
      class="w-80"
      aria-label="Base URL"
    />
  </SettingsRow>
  <SettingsRow label="Timeout (seconds)">
    <Input
      id="duckduckgo-timeout-seconds"
      v-model.number="localConfig.timeout_seconds"
      type="number"
      :min="1"
      class="w-80"
      aria-label="Timeout (seconds)"
    />
  </SettingsRow>
</template>

<script setup lang="ts">
import { reactive, watch } from 'vue'
import { Input, SettingsRow } from '@felinic/ui'

const props = defineProps<{
  modelValue: Record<string, unknown>
}>()

const emit = defineEmits<{
  'update:modelValue': [value: Record<string, unknown>]
}>()

const localConfig = reactive({
  base_url: 'https://html.duckduckgo.com/html/',
  timeout_seconds: 15,
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
