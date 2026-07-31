<template>
  <SettingsRow :label="$t('common.secretId')">
    <Input
      id="sogou-secret-id"
      v-model="localConfig.secret_id"
      type="password"
      class="w-80"
      :aria-label="$t('common.secretId')"
    />
  </SettingsRow>
  <SettingsRow :label="$t('common.secretKey')">
    <Input
      id="sogou-secret-key"
      v-model="localConfig.secret_key"
      type="password"
      class="w-80"
      :aria-label="$t('common.secretKey')"
    />
  </SettingsRow>
  <SettingsRow :label="$t('common.baseUrl')">
    <Input
      id="sogou-base-url"
      v-model="localConfig.base_url"
      class="w-80"
      :aria-label="$t('common.baseUrl')"
      placeholder="wsa.tencentcloudapi.com"
    />
  </SettingsRow>
  <SettingsRow :label="$t('common.timeoutSeconds')">
    <Input
      id="sogou-timeout-seconds"
      v-model.number="localConfig.timeout_seconds"
      type="number"
      :min="1"
      class="w-80"
      :aria-label="$t('common.timeoutSeconds')"
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
  secret_id: '',
  secret_key: '',
  base_url: 'wsa.tencentcloudapi.com',
  timeout_seconds: 15,
})

watch(
  () => props.modelValue,
  (val) => {
    localConfig.secret_id = String(val?.secret_id ?? '')
    localConfig.secret_key = String(val?.secret_key ?? '')
    localConfig.base_url = String(val?.base_url ?? 'wsa.tencentcloudapi.com')
    const timeout = Number(val?.timeout_seconds ?? 15)
    localConfig.timeout_seconds = Number.isFinite(timeout) && timeout > 0 ? timeout : 15
  },
  { immediate: true, deep: true },
)

watch(localConfig, () => {
  emit('update:modelValue', {
    secret_id: localConfig.secret_id,
    secret_key: localConfig.secret_key,
    base_url: localConfig.base_url,
    timeout_seconds: localConfig.timeout_seconds,
  })
}, { deep: true })
</script>
