<template>
  <SettingsRow label="API Key">
    <Input
      id="google-api-key"
      v-model="localConfig.api_key"
      type="password"
      class="w-80"
      aria-label="API Key"
    />
  </SettingsRow>
  <SettingsRow label="Search Engine ID (cx)">
    <Input
      id="google-cx"
      v-model="localConfig.cx"
      class="w-80"
      aria-label="Search Engine ID"
    />
  </SettingsRow>
  <SettingsRow label="Base URL">
    <Input
      id="google-base-url"
      v-model="localConfig.base_url"
      class="w-80"
      aria-label="Base URL"
    />
  </SettingsRow>
  <SettingsRow label="Timeout (seconds)">
    <Input
      id="google-timeout-seconds"
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
  cx: '',
  base_url: 'https://customsearch.googleapis.com/customsearch/v1',
  timeout_seconds: 15,
})

watch(
  () => props.modelValue,
  (val) => {
    localConfig.api_key = String(val?.api_key ?? '')
    localConfig.cx = String(val?.cx ?? '')
    localConfig.base_url = String(val?.base_url ?? 'https://customsearch.googleapis.com/customsearch/v1')
    const timeout = Number(val?.timeout_seconds ?? 15)
    localConfig.timeout_seconds = Number.isFinite(timeout) && timeout > 0 ? timeout : 15
  },
  { immediate: true, deep: true },
)

watch(localConfig, () => {
  emit('update:modelValue', {
    api_key: localConfig.api_key,
    cx: localConfig.cx,
    base_url: localConfig.base_url,
    timeout_seconds: localConfig.timeout_seconds,
  })
}, { deep: true })
</script>
