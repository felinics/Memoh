<template>
  <div class="flex items-center justify-center min-h-screen bg-background text-foreground p-4">
    <Card class="w-full max-w-md">
      <CardContent
        v-if="loading"
        class="flex justify-center py-8"
      >
        <Spinner class="size-8" />
      </CardContent>

      <CardHeader
        v-else
        class="items-center justify-items-center text-center"
      >
        <CircleCheck
          v-if="success"
          class="size-8 text-success"
        />
        <CircleX
          v-else
          class="size-8 text-destructive"
        />
        <CardTitle>{{ success ? t('mcp.oauth.authSuccess') : t('mcp.oauth.authFailed') }}</CardTitle>
        <CardDescription v-if="detail">
          {{ detail }}
        </CardDescription>
      </CardHeader>

      <CardFooter
        v-if="requestId"
        class="justify-center gap-1 text-xs text-muted-foreground"
      >
        <span>{{ t('common.requestId') }}</span>
        <span class="font-mono select-all">{{ requestId }}</span>
        <CopyActionButton
          :text="requestId"
          icon-class="size-3.5"
        />
      </CardFooter>
    </Card>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle, Spinner } from '@felinic/ui'
import { CircleCheck, CircleX } from 'lucide-vue-next'
import { postBotsByBotIdMcpByIdOauthExchange } from '@memohai/sdk'
import CopyActionButton from '@/pages/home/components/copy-action-button.vue'
import { parseMemohError, resolveApiErrorMessage } from '@/utils/api-error'

const route = useRoute()
const { t } = useI18n()

const loading = ref(true)
const success = ref(false)
const detail = ref('')
const requestId = ref('')

// On success the opener takes over and the popup closes itself. A failure
// stays open so the reason and request id can be read and copied.
function notify(status: 'success' | 'error', error?: string) {
  if (!window.opener) return
  window.opener.postMessage({ type: 'mcp-oauth-callback', status, error }, '*')
  if (status === 'success') setTimeout(() => window.close(), 800)
}

function fail(message: string) {
  loading.value = false
  success.value = false
  detail.value = message
  notify('error', message || t('mcp.oauth.authFailed'))
}

onMounted(async () => {
  const code = (route.query.code as string) ?? ''
  const state = (route.query.state as string) ?? ''
  const errorParam = (route.query.error as string) ?? ''
  const errorDesc = (route.query.error_description as string) ?? ''

  if (errorParam) {
    fail(errorDesc ? `${errorParam}: ${errorDesc}` : errorParam)
    return
  }

  if (!code || !state) {
    fail(t('mcp.oauth.callbackMissingParams'))
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
    notify('success')
  } catch (err: unknown) {
    requestId.value = parseMemohError(err)?.requestId ?? ''
    fail(resolveApiErrorMessage(err, ''))
  }
})
</script>
