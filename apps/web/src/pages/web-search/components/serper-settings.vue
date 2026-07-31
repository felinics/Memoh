<template>
  <SettingsRow label="API Key">
    <Input
      id="serper-api-key"
      v-model="localConfig.api_key"
      type="password"
      class="w-80"
      aria-label="API Key"
    />
  </SettingsRow>
  <SettingsRow label="Base URL">
    <Input
      id="serper-base-url"
      v-model="localConfig.base_url"
      class="w-80"
      aria-label="Base URL"
    />
  </SettingsRow>
  <SettingsRow label="Timeout (seconds)">
    <Input
      id="serper-timeout-seconds"
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
  api_key: '',
  base_url: 'https://google.serper.dev/search',
  timeout_seconds: 15,
})

watch(
  () => props.modelValue,
  (val) => {
    localConfig.api_key = String(val?.api_key ?? '')
    localConfig.base_url = String(val?.base_url ?? 'https://google.serper.dev/search')
    const timeout = Number(val?.timeout_seconds ?? 15)
    localConfig.timeout_seconds = Number.isFinite(timeout) && timeout > 0 ? timeout : 15
  },
  { immediate: true, deep: true },
)

watch(localConfig, () => {
  emit('update:modelValue', {
    api_key: localConfig.api_key,
    base_url: localConfig.base_url,
    timeout_seconds: localConfig.timeout_seconds,
  })
}, { deep: true })
</script>
