<template>
  <div class="flex items-center justify-center min-h-screen bg-background text-foreground">
    <div class="text-center space-y-3 p-8 max-w-md">
      <Spinner
        v-if="loading"
        class="mx-auto size-8"
      />
      <CircleCheck
        v-else-if="success"
        class="size-8 text-success"
      />
      <CircleX
        v-else
        class="size-8 text-destructive"
      />
      <p class="text-xs text-muted-foreground">
        {{ message }}
      </p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Spinner } from '@felinic/ui'
import { CircleCheck, CircleX } from 'lucide-vue-next'
import { postBotsByBotIdMcpByIdOauthExchange } from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'

const route = useRoute()
const { t } = useI18n()

const loading = ref(true)
const success = ref(false)
const message = ref(t('common.loading'))

function notify(status: 'success' | 'error', error?: string) {
  if (window.opener) {
    window.opener.postMessage({ type: 'mcp-oauth-callback', status, error }, '*')
    setTimeout(() => window.close(), 800)
  }
}

onMounted(async () => {
  const code = (route.query.code as string) ?? ''
  const state = (route.query.state as string) ?? ''
  const errorParam = (route.query.error as string) ?? ''
  const errorDesc = (route.query.error_description as string) ?? ''

  if (errorParam) {
    loading.value = false
    success.value = false
    message.value = `${errorParam}: ${errorDesc}`
    notify('error', message.value)
    return
  }

  if (!code || !state) {
    loading.value = false
    success.value = false
    message.value = t('mcp.oauth.callbackMissingParams')
    notify('error', message.value)
    return
  }

  try {
    await postBotsByBotIdMcpByIdOauthExchange({
      path: { bot_id: '-', id: '-' },
      body: { code, state },
      throwOnError: true,
    })
    loading.value = false
    success.value = true
    message.value = t('mcp.oauth.authSuccess')
    notify('success')
  } catch (err: unknown) {
    loading.value = false
    success.value = false
    const errMsg = resolveApiErrorMessage(err, t('mcp.oauth.authFailed')) || t('mcp.oauth.authFailed')
    message.value = errMsg
    notify('error', errMsg)
  }
})
</script>
