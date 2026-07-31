<template>
  <SettingsRow :label="$t('webSearch.accountId')">
    <Input
      id="cloudflare-account-id"
      v-model="localConfig.account_id"
      class="w-80"
      :aria-label="$t('webSearch.accountId')"
    />
  </SettingsRow>
  <SettingsRow :label="$t('webSearch.apiToken')">
    <Input
      id="cloudflare-api-token"
      v-model="localConfig.api_token"
      type="password"
      class="w-80"
      :aria-label="$t('webSearch.apiToken')"
    />
  </SettingsRow>
  <SettingsRow :label="$t('common.baseUrl')">
    <Input
      id="cloudflare-base-url"
      v-model="localConfig.base_url"
      class="w-80"
      :aria-label="$t('common.baseUrl')"
    />
  </SettingsRow>
  <SettingsRow :label="$t('common.timeoutSeconds')">
    <Input
      id="cloudflare-timeout-seconds"
      v-model.number="localConfig.timeout_seconds"
      type="number"
      class="w-40"
      :min="1"
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
  account_id: '',
  api_token: '',
  base_url: 'https://api.cloudflare.com/client/v4',
  timeout_seconds: 30,
})

watch(
  () => props.modelValue,
  (val) => {
    localConfig.account_id = String(val?.account_id ?? '')
    localConfig.api_token = String(val?.api_token ?? '')
    localConfig.base_url = String(val?.base_url ?? 'https://api.cloudflare.com/client/v4')
    const timeout = Number(val?.timeout_seconds ?? 30)
    localConfig.timeout_seconds = Number.isFinite(timeout) && timeout > 0 ? timeout : 30
  },
  { immediate: true, deep: true },
)

watch(localConfig, () => {
  emit('update:modelValue', {
    account_id: localConfig.account_id,
    api_token: localConfig.api_token,
    base_url: localConfig.base_url,
    timeout_seconds: localConfig.timeout_seconds,
  })
}, { deep: true })
</script>
