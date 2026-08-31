<template>
  <div class="space-y-8">
    <SettingsSection :title="$t('bots.agent.runtimeSetup')">
      <SettingsRow
        :label="$t('bots.agent.authMode')"
        :description="authDescription"
      >
        <Select
          :model-value="config.auth"
          :disabled="credentialConnected"
          @update:model-value="setAuthMode"
        >
          <SelectTrigger class="w-full sm:w-56">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem
              v-for="option in authOptions"
              :key="option.value"
              :value="option.value"
            >
              {{ option.label }}
            </SelectItem>
          </SelectContent>
        </Select>
      </SettingsRow>

      <SettingsRow
        v-if="config.auth === 'api_key' || config.auth === 'oauth_token'"
        :label="config.auth === 'api_key' ? $t('bots.agent.apiKey') : $t('bots.agent.oauthToken')"
        :description="config.auth === 'api_key' ? $t('bots.agent.apiKeyDescription') : $t('bots.agent.oauthTokenDescription')"
        stack="sm"
      >
        <div class="flex w-full flex-col gap-2 sm:w-96 sm:flex-row">
          <PasswordInput
            v-model="credentialSecret"
            autocomplete="new-password"
            class="min-w-0 flex-1"
            :placeholder="$t('bots.settings.agentCredentialSecretPlaceholder')"
            @keydown.enter.prevent="saveCredential"
          />
          <Button
            type="button"
            size="sm"
            :loading="savingCredential"
            :disabled="!credentialSecret.trim()"
            @click="saveCredential"
          >
            {{ credentialConnected ? $t('bots.settings.agentCredentialReplace') : $t('bots.settings.agentCredentialSave') }}
          </Button>
          <ConfirmPopover
            v-if="credentialConnected"
            :title="$t('bots.settings.agentCredentialDisconnectConfirm')"
            :cancel-text="$t('common.cancel')"
            :confirm-text="$t('bots.settings.agentCredentialDisconnect')"
            variant="destructive"
            @confirm="disconnectCredential"
          >
            <template #trigger>
              <Button
                type="button"
                variant="outline"
                size="sm"
                :disabled="savingCredential"
              >
                {{ $t('bots.settings.agentCredentialDisconnect') }}
              </Button>
            </template>
          </ConfirmPopover>
        </div>
      </SettingsRow>

      <SettingsRow
        v-if="config.auth === 'api_key' || config.auth === 'oauth_token'"
        :label="$t('common.baseUrl')"
        stack="sm"
      >
        <Input
          v-model="config.base_url"
          class="w-full sm:w-80"
          :placeholder="baseUrlPlaceholder"
          @blur="commitConfig"
          @keydown.enter.prevent="commitConfig"
        />
      </SettingsRow>

      <SettingsRow
        :label="$t('bots.agent.defaultModel')"
        :description="$t('bots.agent.defaultModelDescription')"
        stack="sm"
      >
        <Input
          v-model="config.model"
          class="w-full sm:w-80"
          :placeholder="$t('bots.agent.defaultModelPlaceholder')"
          @blur="commitConfig"
          @keydown.enter.prevent="commitConfig"
        />
      </SettingsRow>
    </SettingsSection>

    <SettingsSection
      v-if="isCodex && config.auth === 'chatgpt'"
      :title="$t('bots.agent.account')"
    >
      <SettingsRow
        :label="$t('bots.agent.chatgptAccount')"
        :description="credentialConnected ? $t('bots.agent.chatgptAccountConnectedDescription') : $t('bots.agent.chatgptAccountDescription')"
      >
        <div class="flex gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            :loading="authorizing"
            loading-mode="manual"
            @click="deviceLogin ? cancelDeviceLogin() : startDeviceLogin()"
          >
            <template v-if="deviceLogin">
              {{ $t('common.cancel') }}
            </template>
            <template v-else>
              <KeyRound />
              {{ credentialConnected ? $t('bots.agent.reconnect') : $t('provider.oauth.connect') }}
            </template>
          </Button>
          <ConfirmPopover
            v-if="credentialConnected"
            :title="$t('bots.settings.agentCredentialDisconnectConfirm')"
            :cancel-text="$t('common.cancel')"
            :confirm-text="$t('bots.settings.agentCredentialDisconnect')"
            variant="destructive"
            @confirm="disconnectCredential"
          >
            <template #trigger>
              <Button
                type="button"
                variant="outline"
                size="sm"
                :disabled="authorizing"
              >
                {{ $t('bots.settings.agentCredentialDisconnect') }}
              </Button>
            </template>
          </ConfirmPopover>
        </div>
      </SettingsRow>

      <div
        v-if="deviceLogin"
        class="mx-4 border-b border-border py-6 last:border-b-0"
      >
        <DeviceCodePanel
          :code="deviceLogin.user_code"
          :verification-uri="deviceLogin.verification_url"
          :hint="$t('bots.agent.codexDeviceHint')"
          :retry-loading="authorizing"
          :copy-and-open-label="$t('deviceCode.copyAndOpen')"
          :retry-label="$t('deviceCode.retry')"
          :expired-label="$t('deviceCode.codeExpired')"
          :expires-in-label="(time: string) => $t('deviceCode.expiresIn', { time })"
          :copy-failed-message="$t('deviceCode.copyFailed')"
          @retry="startDeviceLogin"
        />
      </div>
    </SettingsSection>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQueryCache } from '@pinia/colada'
import {
  Button,
  ConfirmPopover,
  DeviceCodePanel,
  Input,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  SettingsRow,
  SettingsSection,
  toast,
} from '@felinic/ui'
import { KeyRound } from 'lucide-vue-next'
import {
  deleteBotsByBotIdAgentsByIdCredential,
  patchBotsByBotIdAgentsById,
  postBotsByBotIdAgentsByIdCodexLoginDeviceAuthorize,
  postBotsByBotIdAgentsByIdCodexLoginDeviceCancel,
  postBotsByBotIdAgentsByIdCodexLoginDevicePoll,
  putBotsByBotIdAgentsByIdCredential,
  type BotagentsBotAgent,
} from '@memohai/sdk'
import PasswordInput from '@/components/password-input/index.vue'
import { resolveApiErrorMessage } from '@/utils/api-error'
import {
  BOT_AGENT_RUNTIME_CODEX,
  normalizeBotAgentRuntime,
} from '@/utils/bot-agent'

interface DirectAgentConfig {
  auth: string
  base_url: string
  model: string
  reasoning_effort: string
}

interface DeviceLogin {
  login_id: string
  user_code: string
  verification_url: string
}

const props = defineProps<{
  botId: string
  agent: BotagentsBotAgent
}>()
const emit = defineEmits<{ authorized: [] }>()
const { t } = useI18n()
const queryCache = useQueryCache()

const runtime = computed(() => normalizeBotAgentRuntime(props.agent.runtime))
const isCodex = computed(() => runtime.value === BOT_AGENT_RUNTIME_CODEX)
const config = reactive<DirectAgentConfig>({ auth: '', base_url: '', model: '', reasoning_effort: '' })
const credentialSecret = ref('')
const savingCredential = ref(false)
const credentialConnected = computed(() => !!props.agent.agent_credential_id)

function readConfig() {
  const source = props.agent.metadata ?? {}
  config.auth = String(source.auth ?? '').trim()
  config.base_url = String(source.base_url ?? '')
  config.model = String(source.model ?? '')
  config.reasoning_effort = String(source.reasoning_effort ?? '')
}

watch([() => props.agent.metadata, runtime], readConfig, { immediate: true })

const authOptions = computed(() => isCodex.value
  ? [
      { value: 'chatgpt', label: t('bots.agent.authChatGPT') },
      { value: 'api_key', label: t('bots.agent.apiKey') },
    ]
  : [
      { value: 'workspace', label: t('bots.agent.authWorkspace') },
      { value: 'api_key', label: t('bots.agent.apiKey') },
      { value: 'oauth_token', label: t('bots.agent.authOAuthToken') },
    ])

const authDescription = computed(() => {
  if (config.auth === 'workspace') return t('bots.agent.authWorkspaceDescription')
  if (config.auth === 'chatgpt') return t('bots.agent.authChatGPTDescription')
  if (config.auth === 'oauth_token') return t('bots.agent.authOAuthTokenDescription')
  return t('bots.agent.authApiKeyDescription')
})

const baseUrlPlaceholder = computed(() => isCodex.value
  ? 'https://api.openai.com/v1'
  : 'https://api.anthropic.com')

async function refreshAgent() {
  await queryCache.invalidateQueries({ key: ['bot-agents', props.botId] })
  emit('authorized')
}

async function commitConfig(): Promise<boolean> {
  const metadata: Record<string, unknown> = {
    ...props.agent.metadata,
    provider: runtime.value,
    auth: config.auth,
  }
  if (config.base_url.trim()) metadata.base_url = config.base_url.trim()
  else delete metadata.base_url
  if (config.model.trim()) metadata.model = config.model.trim()
  else delete metadata.model
  if (isCodex.value && config.reasoning_effort.trim()) metadata.reasoning_effort = config.reasoning_effort.trim()
  else delete metadata.reasoning_effort
  try {
    await patchBotsByBotIdAgentsById({
      path: { bot_id: props.botId, id: props.agent.id! },
      body: { metadata },
      throwOnError: true,
    })
    await refreshAgent()
    return true
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
    return false
  }
}

async function setAuthMode(value: unknown) {
  if (credentialConnected.value || typeof value !== 'string' || !value || value === config.auth) return
  const previous = config.auth
  config.auth = value
  credentialSecret.value = ''
  if (!await commitConfig()) {
    config.auth = previous
  }
}

async function saveCredential() {
  const secret = credentialSecret.value.trim()
  if (!secret) return
  const authKind = isCodex.value
    ? 'openai_api_key'
    : config.auth === 'oauth_token' ? 'claude_code_oauth' : 'anthropic_api_key'
  const secretKey = config.auth === 'oauth_token' ? 'oauth_token' : 'api_key'
  savingCredential.value = true
  try {
    await putBotsByBotIdAgentsByIdCredential({
      path: { bot_id: props.botId, id: props.agent.id! },
      body: { auth_kind: authKind, secret: { [secretKey]: secret } },
      throwOnError: true,
    })
    credentialSecret.value = ''
    await refreshAgent()
    toast.success(t('bots.settings.agentCredentialSaved'))
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
  } finally {
    savingCredential.value = false
  }
}

async function disconnectCredential() {
  try {
    await deleteBotsByBotIdAgentsByIdCredential({
      path: { bot_id: props.botId, id: props.agent.id! },
      throwOnError: true,
    })
    await refreshAgent()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
  }
}

const deviceLogin = ref<DeviceLogin | null>(null)
const authorizing = ref(false)
let pollTimer: ReturnType<typeof setTimeout> | undefined

function stopPolling() {
  if (pollTimer === undefined) return
  clearTimeout(pollTimer)
  pollTimer = undefined
}

async function pollDeviceLogin(loginId: string) {
  try {
    const { data } = await postBotsByBotIdAgentsByIdCodexLoginDevicePoll({
      path: { bot_id: props.botId, id: props.agent.id! },
      body: { login_id: loginId },
      throwOnError: true,
    })
    if (deviceLogin.value?.login_id !== loginId) return
    if (data.status === 'success') {
      stopPolling()
      deviceLogin.value = null
      await refreshAgent()
      toast.success(t('bots.agent.codexLoginSuccess'))
      return
    }
    if (data.status === 'error' || data.status === 'unknown') {
      stopPolling()
      deviceLogin.value = null
      toast.error(t('bots.agent.codexLoginFailed'))
      return
    }
    pollTimer = setTimeout(() => void pollDeviceLogin(loginId), 2000)
  } catch (error) {
    stopPolling()
    deviceLogin.value = null
    toast.error(resolveApiErrorMessage(error, t('bots.agent.codexLoginFailed')))
  }
}

async function startDeviceLogin() {
  if (authorizing.value) return
  stopPolling()
  authorizing.value = true
  try {
    const { data } = await postBotsByBotIdAgentsByIdCodexLoginDeviceAuthorize({
      path: { bot_id: props.botId, id: props.agent.id! },
      throwOnError: true,
    })
    deviceLogin.value = {
      login_id: data.login_id,
      user_code: data.user_code,
      verification_url: data.verification_url,
    }
    void pollDeviceLogin(data.login_id)
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.agent.codexLoginFailed')))
  } finally {
    authorizing.value = false
  }
}

async function cancelDeviceLogin() {
  const loginId = deviceLogin.value?.login_id
  if (!loginId || authorizing.value) return
  stopPolling()
  deviceLogin.value = null
  try {
    await postBotsByBotIdAgentsByIdCodexLoginDeviceCancel({
      path: { bot_id: props.botId, id: props.agent.id! },
      body: { login_id: loginId },
      throwOnError: true,
    })
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.agent.codexLoginFailed')))
  }
}

onBeforeUnmount(stopPolling)
</script>
