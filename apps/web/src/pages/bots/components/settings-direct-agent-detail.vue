<template>
  <div class="space-y-8">
    <SettingsSection :title="$t('bots.agent.runtimeSetup')">
      <SettingsRow
        :label="$t('bots.agent.authMode')"
        :description="authDescription"
      >
        <Select
          :model-value="config.auth"
          :disabled="authModeLocked"
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
        <AgentCredentialInput
          v-model="credentialSecret"
          :loading="savingCredential"
          :connected="credentialConnected"
          :placeholder="config.auth === 'oauth_token' ? $t('bots.agent.oauthToken') : undefined"
          class="sm:w-96"
          @save="saveCredential"
        >
          <AgentCredentialDisconnect
            v-if="credentialConnected"
            :disabled="savingCredential"
            @confirm="disconnectCredential"
          />
        </AgentCredentialInput>
      </SettingsRow>

      <SettingsRow
        v-if="baseUrlPlaceholder && (config.auth === 'api_key' || config.auth === 'oauth_token')"
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
      <SettingsRow
        v-if="defaultModes?.supported"
        :label="$t('bots.agent.defaultPermissionMode')"
        :description="$t('bots.agent.defaultPermissionModeDescription')"
        stack="sm"
      >
        <Select
          :model-value="config.permission_mode || defaultModes.current_mode_id"
          @update:model-value="setDefaultPermission"
        >
          <SelectTrigger class="w-full sm:w-56">
            <SelectValue>{{ defaultMode?.name }}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem
              v-for="mode in defaultModes.available_modes"
              :key="mode.id"
              :value="mode.id!"
            >
              <span :class="mode.warning ? 'text-warning-foreground' : ''">{{ mode.name }}</span>
            </SelectItem>
          </SelectContent>
        </Select>
      </SettingsRow>
    </SettingsSection>

    <!-- Codex and Grok Build sign their account in on the Agent's own
         process: the runtime obtains the device code, writes its login into
         the workspace, and keeps it there. -->
    <SettingsSection
      v-if="deviceLoginAuth"
      :title="$t('bots.agent.account')"
    >
      <DeviceLoginPanel
        :auth="deviceLoginAuth"
        :authorized="credentialConnected"
        :account="runtimeSignIn ? runtimeAccount.data.value?.email : undefined"
        :note="accountNote"
        :disabled="!agentEnabled"
        :authorizing="authorizing"
        :device-login="deviceLogin"
        @connect="startDeviceLogin"
        @cancel="cancelDeviceLogin"
      >
        <AgentCredentialDisconnect
          v-if="credentialConnected"
          :disabled="authorizing"
          @confirm="disconnectCredential"
        />
      </DeviceLoginPanel>

      <CodexUsageRows
        v-if="isCodex && credentialConnected && !deviceLogin"
        :bot-id="botId"
        :bot-agent-id="agent.id!"
      />
    </SettingsSection>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useQuery, useQueryCache } from '@pinia/colada'
import {
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
import {
  deleteBotsByBotIdAgentsByIdCredential,
  getBotsByBotIdAgentsByIdAccount,
  getBotsByBotIdAgentsByIdRuntimeControls,
  patchBotsByBotIdAgentsById,
  postBotsByBotIdAgentsByIdLoginDeviceAuthorize,
  postBotsByBotIdAgentsByIdLoginDeviceCancel,
  postBotsByBotIdAgentsByIdLoginDevicePoll,
  putBotsByBotIdAgentsByIdCredential,
  type BotagentsBotAgent,
} from '@memohai/sdk'
import AgentCredentialDisconnect from './agent-credential-disconnect.vue'
import AgentCredentialInput from './agent-credential-input.vue'
import CodexUsageRows from './codex-usage-rows.vue'
import DeviceLoginPanel from './device-login-panel.vue'
import { localizeRuntimeControls } from '@/utils/runtime-control-presentation'
import { isApiErrorCode, resolveApiErrorMessage } from '@/utils/api-error'
import {
  AGENT_AUTH_MODE_COPY,
  BOT_AGENT_RUNTIME_CLAUDE_CODE,
  BOT_AGENT_RUNTIME_CODEX,
  DEVICE_LOGINS,
  DIRECT_BOT_AGENT_API_KEY_AUTH_KIND,
  DIRECT_BOT_AGENT_AUTH_MODES,
  DIRECT_BOT_AGENT_DEPENDENCY_ID,
  isAgentAuthMode,
  isDeviceLoginAuthMode,
  isDirectBotAgentRuntime,
  normalizeBotAgentRuntime,
} from '@/utils/bot-agent'
import { externalAgentDisplayName } from '@/utils/external-agent'

interface DirectAgentConfig {
  auth: string
  base_url: string
  model: string
  permission_mode: string
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
const { t, te } = useI18n()
const router = useRouter()
const queryCache = useQueryCache()

const runtime = computed(() => normalizeBotAgentRuntime(props.agent.runtime))
const isCodex = computed(() => runtime.value === BOT_AGENT_RUNTIME_CODEX)
const isClaudeCode = computed(() => runtime.value === BOT_AGENT_RUNTIME_CLAUDE_CODE)
const config = reactive<DirectAgentConfig>({ auth: '', base_url: '', model: '', reasoning_effort: '', permission_mode: '' })
const agentEnabled = computed(() => props.agent.enabled !== false)
const defaultControls = useQuery({
  key: () => ['bot-agent-runtime-controls', props.botId, props.agent.id ?? '', props.agent.metadata?.permission_mode ?? ''],
  enabled: () => agentEnabled.value && !!props.agent.id,
  query: async ({ signal }) => {
    const { data } = await getBotsByBotIdAgentsByIdRuntimeControls({
      path: { bot_id: props.botId, id: props.agent.id! },
      signal, throwOnError: true,
    })
    return data
  },
})
const defaultModes = computed(() => localizeRuntimeControls(
  defaultControls.data.value,
  (key, fallback) => te(key) || te(key, 'en') ? t(key) : fallback,
)?.modes)
const defaultMode = computed(() => defaultModes.value?.available_modes?.find(mode => mode.id === (config.permission_mode || defaultModes.value?.current_mode_id)))
async function setDefaultPermission(value: unknown) {
  if (typeof value !== 'string') return
  const previous = config.permission_mode
  config.permission_mode = value
  if (!await commitConfig()) config.permission_mode = previous
}
const credentialSecret = ref('')
const savingCredential = ref(false)
// A device-code sign-in (Codex's ChatGPT account, a Grok account) is kept by
// the runtime in the workspace, so only the runtime can say whether it is
// signed in.
const runtimeSignIn = computed(() => isDeviceLoginAuthMode(config.auth))
const runtimeAccount = useQuery({
  key: () => ['agent-account', props.botId, props.agent.id ?? ''],
  enabled: () => agentEnabled.value && runtimeSignIn.value && !!props.agent.id,
  // Reading it starts Codex, so only opening the page and signing in or out do.
  refetchOnWindowFocus: false,
  refetchOnReconnect: false,
  query: async ({ signal }) => {
    const { data } = await getBotsByBotIdAgentsByIdAccount({
      path: { bot_id: props.botId, id: props.agent.id! },
      signal, throwOnError: true,
    })
    return data
  },
})
const credentialConnected = computed(() => runtimeSignIn.value
  ? agentEnabled.value && !!runtimeAccount.data.value?.signed_in
  : !!props.agent.agent_credential_id)
const accountNote = computed(() => {
  if (!agentEnabled.value) return t('bots.agent.enableToSignIn')
  if (!runtimeSignIn.value || runtimeAccount.data.value) return undefined
  if (runtimeAccount.error.value) return resolveApiErrorMessage(runtimeAccount.error.value, t('bots.agent.signInCheckFailed'))
  return t('bots.agent.checkingSignIn')
})

const authOptions = computed(() => (isDirectBotAgentRuntime(runtime.value) ? DIRECT_BOT_AGENT_AUTH_MODES[runtime.value] : [])
  .map(value => ({ value, label: t(AGENT_AUTH_MODE_COPY[value].label) })))
// The auth mode whose account signs in with a device code on this Agent.
const deviceLoginAuth = computed(() => isDeviceLoginAuthMode(config.auth) ? config.auth : undefined)

function readConfig() {
  const source = props.agent.metadata ?? {}
  config.auth = String(source.auth ?? '').trim()
  config.base_url = String(source.base_url ?? '')
  config.model = String(source.model ?? '')
  config.reasoning_effort = String(source.reasoning_effort ?? '')
  config.permission_mode = String(source.permission_mode ?? '')
}

watch([() => props.agent.metadata, runtime], readConfig, { immediate: true })

const authDescription = computed(() => t(AGENT_AUTH_MODE_COPY[isAgentAuthMode(config.auth) ? config.auth : 'api_key'].description))

// Empty for a runtime that takes no base URL override; the row is then hidden.
const baseUrlPlaceholder = computed(() => isCodex.value
  ? 'https://api.openai.com/v1'
  : isClaudeCode.value ? 'https://api.anthropic.com' : '')

async function refreshAgent() {
  // Usage is not refreshed here: its rows show only while Codex is signed in
  // and read it when they appear.
  await Promise.all([
    queryCache.invalidateQueries({ key: ['bot-agents', props.botId] }),
    queryCache.invalidateQueries({ key: ['agent-account', props.botId, props.agent.id ?? ''] }),
  ])
  emit('authorized')
}

async function commitConfig(): Promise<boolean> {
  const metadata: Record<string, unknown> = {
    ...props.agent.metadata,
    provider: runtime.value,
    auth: config.auth,
  }
  if (config.permission_mode) metadata.permission_mode = config.permission_mode
  if (config.base_url.trim()) metadata.base_url = config.base_url.trim()
  else delete metadata.base_url
  if (config.model.trim()) metadata.model = config.model.trim()
  else delete metadata.model
  if (!isClaudeCode.value && config.reasoning_effort.trim()) metadata.reasoning_effort = config.reasoning_effort.trim()
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
  if (authModeLocked.value || typeof value !== 'string' || !value || value === config.auth) return
  const previous = config.auth
  config.auth = value
  credentialSecret.value = ''
  if (!await commitConfig()) {
    config.auth = previous
  }
}

async function saveCredential() {
  const secret = credentialSecret.value.trim()
  if (!secret || !isDirectBotAgentRuntime(runtime.value)) return
  const authKind = config.auth === 'oauth_token'
    ? 'claude_code_oauth'
    : DIRECT_BOT_AGENT_API_KEY_AUTH_KIND[runtime.value]
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

// Changing mode while a credential is attached or about to be would strand it.
const authModeLocked = computed(() => credentialConnected.value || !!deviceLogin.value)

function stopPolling() {
  if (pollTimer === undefined) return
  clearTimeout(pollTimer)
  pollTimer = undefined
}

// The sign-in copy of the auth mode a login was started under; the mode is
// locked while one is pending.
const loginCopy = computed(() => DEVICE_LOGINS[deviceLoginAuth.value ?? 'chatgpt'])

async function pollDeviceLogin(loginId: string) {
  try {
    const { data } = await postBotsByBotIdAgentsByIdLoginDevicePoll({
      path: { bot_id: props.botId, id: props.agent.id! },
      body: { login_id: loginId },
      throwOnError: true,
    })
    if (deviceLogin.value?.login_id !== loginId) return
    if (data.status === 'success') {
      stopPolling()
      deviceLogin.value = null
      await refreshAgent()
      toast.success(t(loginCopy.value.success))
      return
    }
    if (data.status === 'error' || data.status === 'unknown') {
      stopPolling()
      deviceLogin.value = null
      toast.error(t(loginCopy.value.failed))
      return
    }
    pollTimer = setTimeout(() => void pollDeviceLogin(loginId), 2000)
  } catch (error) {
    stopPolling()
    deviceLogin.value = null
    toast.error(resolveApiErrorMessage(error, t(loginCopy.value.failed)))
  }
}

async function startDeviceLogin() {
  if (!agentEnabled.value || authorizing.value || !isDirectBotAgentRuntime(runtime.value)) return
  stopPolling()
  authorizing.value = true
  try {
    const { data } = await postBotsByBotIdAgentsByIdLoginDeviceAuthorize({
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
    if (isApiErrorCode(error, 'agent_dependency_missing')) {
      const dependencyId = DIRECT_BOT_AGENT_DEPENDENCY_ID[runtime.value]
      toast.error(t('chat.externalAgent.dependencyMissing', { dep_id: externalAgentDisplayName(runtime.value, runtime.value) }), {
        action: {
          label: t('chat.externalAgent.dependencyOpen'),
          onClick: () => {
            void router.push({
              name: 'bot-detail',
              params: { botName: props.botId },
              query: { tab: 'dependencies', dependency_id: dependencyId },
            }).catch(() => {})
          },
        },
      })
    } else {
      toast.error(resolveApiErrorMessage(error, t(loginCopy.value.failed)))
    }
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
    await postBotsByBotIdAgentsByIdLoginDeviceCancel({
      path: { bot_id: props.botId, id: props.agent.id! },
      body: { login_id: loginId },
      throwOnError: true,
    })
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t(loginCopy.value.failed)))
  }
}

onBeforeUnmount(stopPolling)
</script>
