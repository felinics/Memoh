<!-- eslint-disable vue/no-mutating-props -->
<template>
  <div class="space-y-8">
    <section class="flex items-center gap-3 rounded-[var(--radius-menu-shell)] border border-border bg-card px-4 py-3">
      <span class="flex size-9 shrink-0 items-center justify-center">
        <component
          :is="acpAgentIcon(profile.id, true)"
          class="size-5"
        />
      </span>
      <h2 class="truncate text-sm font-semibold">
        {{ profile.display_name || profile.id }}
      </h2>
    </section>

    <SettingsSection :title="isGenericACP ? t('bots.settings.acpLaunchSection') : undefined">
      <div class="space-y-5 p-4">
        <SegmentedControl
          v-if="!isGenericACP"
          :model-value="agent.setup_mode"
          :items="setupModeItems"
          :aria-label="$t('bots.settings.acpSetupMode')"
          class="w-full sm:w-fit"
          @update:model-value="(mode) => setSetupMode(String(mode))"
        />

        <!-- OAuth 模式下这里可能一个托管字段都不剩(Codex),空的字段栈会白占一格
             space-y —— 有字段才渲染。 -->
        <AcpManagedFields
          v-if="agent.setup_mode !== 'self' && visibleManagedFields.length > 0"
          :profile="profile"
          :managed="agent.managed"
          :fields="visibleManagedFields"
          settings-mode
          @field-commit="commitForm"
        />

        <p
          v-else-if="agent.setup_mode === 'self'"
          class="break-words text-sm text-muted-foreground"
        >
          {{ selfModeHint }}
        </p>
        <Button
          v-if="isHermesSelfConfirmVisible"
          size="sm"
          class="mt-3"
          @click="confirmSelfMode"
        >
          {{ $t('bots.settings.acpHermesSelfModeConfirm') }}
        </Button>
      </div>
    </SettingsSection>

    <!-- 账号:与 providers 页的 OAuth 卡片同形 —— 一行账号状态 + 行内动作,等待
         输码时才在卡片内追加居中的验证码块。授权轮询在后台静默完成。 -->
    <SettingsSection
      v-if="oauthSectionVisible"
      :title="$t('provider.oauth.sectionTitle')"
    >
      <!-- AutoHeight:验证码块出现/收起时让卡片平滑生长,不硬切。 -->
      <AutoHeight>
        <template v-if="isCodex">
          <!-- 首次加载:借行高稳住卡片,状态到达时不跳动。
               ui-allow-shape: skeleton borrowing the row height, not a data row. -->
          <div
            v-if="codexOAuthStatusLoading && !codexOAuthStatus"
            class="mx-4 flex min-h-[3.75rem] items-center justify-center py-3"
          >
            <Spinner class="size-5 text-muted-foreground" />
          </div>

          <!-- 无法把授权写进当前工作区:说明原因,没有可给的动作。 -->
          <SettingsRow
            v-else-if="codexOAuthStatus && !codexOAuthStatus.configured"
            :label="$t('bots.settings.acpCodexAccount')"
            :description="$t('bots.settings.acpOAuthUnavailable')"
          />

          <template v-else>
            <SettingsRow
              :label="$t('bots.settings.acpCodexAccount')"
              :description="codexAccountDescription"
            >
              <Button
                type="button"
                variant="outline"
                size="sm"
                class="shrink-0"
                :disabled="authorizingCodexDevice"
                :loading="authorizingCodexDevice"
                loading-mode="manual"
                @click="codexDevicePending ? handleCancelCodexDeviceAuthorization() : handleAuthorizeDevice()"
              >
                <!-- 三态 label:连接 → 连接中…(按下变长,给等待一个视觉挽留点)
                     → 取消(码到手收短)。已连接时说"重新连接",因为按下会换掉
                     现有账号,而不是首次接上。 -->
                <LabelSwap :active="codexButtonState">
                  <template #connect>
                    <KeyRound />
                    {{ $t('provider.oauth.connect') }}
                  </template>
                  <template #reconnect>
                    <KeyRound />
                    {{ $t('bots.settings.acpOAuthReconnect') }}
                  </template>
                  <template #connecting>
                    <Spinner />
                    {{ $t('provider.oauth.connecting') }}
                  </template>
                  <template #cancel>
                    {{ $t('common.cancel') }}
                  </template>
                </LabelSwap>
              </Button>
            </SettingsRow>

            <!-- 输码时刻交给 owner;这层 wrapper 只负责它在卡片里的定位。 -->
            <div
              v-if="codexDevicePanelSession"
              class="mx-4 border-b border-border py-6 last:border-b-0"
            >
              <DeviceCodePanel
                :code="codexDevicePanelSession.user_code"
                :verification-uri="codexDevicePanelSession.verification_url"
                :expires-at="codexDevicePanelSession.expires_at ?? ''"
                :hint="$t('bots.settings.acpCodexDeviceHint')"
                :retry-loading="authorizingCodexDevice"
                :copy-and-open-label="$t('deviceCode.copyAndOpen')"
                :retry-label="$t('deviceCode.retry')"
                :expired-label="$t('deviceCode.codeExpired')"
                :expires-in-label="(time: string) => $t('deviceCode.expiresIn', { time })"
                :copy-failed-message="$t('deviceCode.copyFailed')"
                @retry="handleAuthorizeDevice"
              />
            </div>
          </template>
        </template>

        <template v-else-if="isClaude">
          <!-- ui-allow-shape: skeleton borrowing the row height, not a data row. -->
          <div
            v-if="claudeOAuthStatusLoading && !claudeOAuthStatus"
            class="mx-4 flex min-h-[3.75rem] items-center justify-center py-3"
          >
            <Spinner class="size-5 text-muted-foreground" />
          </div>

          <SettingsRow
            v-else-if="claudeOAuthStatus && !claudeOAuthStatus.configured"
            :label="$t('bots.settings.acpClaudeAccount')"
            :description="$t('bots.settings.acpClaudeOAuthUnavailable')"
          />

          <template v-else>
            <SettingsRow
              :label="$t('bots.settings.acpClaudeAccount')"
              :description="claudeAccountDescription"
            >
              <Button
                type="button"
                variant="outline"
                size="sm"
                class="shrink-0"
                :disabled="authorizingClaudeOAuth"
                :loading="authorizingClaudeOAuth"
                loading-mode="manual"
                @click="handleAuthorizeClaude"
              >
                <LabelSwap :active="claudeButtonState">
                  <template #connect>
                    <KeyRound />
                    {{ $t('provider.oauth.connect') }}
                  </template>
                  <template #reconnect>
                    <KeyRound />
                    {{ $t('bots.settings.acpOAuthReconnect') }}
                  </template>
                  <template #connecting>
                    <Spinner />
                    {{ $t('provider.oauth.connecting') }}
                  </template>
                </LabelSwap>
              </Button>
            </SettingsRow>

            <!-- Claude 走的是"授权页给码、回来粘贴"——不是设备码,所以是行内工具块
                 (说明 + 输入行)的 py-4 档,不是英雄面板的 py-6。 -->
            <div
              v-if="claudeExchangeVisible"
              class="mx-4 space-y-2.5 border-b border-border py-4 last:border-b-0"
            >
              <p class="text-body text-muted-foreground">
                {{ $t('bots.settings.acpClaudeOAuthCodeHint') }}
              </p>
              <div class="flex flex-col gap-2 sm:flex-row">
                <Input
                  v-model="claudeOAuthCode"
                  :placeholder="$t('bots.settings.acpClaudeOAuthCodePlaceholder')"
                  class="min-w-0 flex-1"
                />
                <Button
                  type="button"
                  class="shrink-0"
                  :loading="exchangingClaudeOAuth"
                  @click="handleExchangeClaudeOAuth"
                >
                  {{ $t('bots.settings.acpClaudeOAuthExchange') }}
                </Button>
              </div>
            </div>
          </template>
        </template>
      </AutoHeight>
    </SettingsSection>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQueryCache } from '@pinia/colada'
import {
  AutoHeight,
  Button,
  DeviceCodePanel,
  Input,
  LabelSwap,
  SegmentedControl,
  SettingsRow,
  SettingsSection,
  Spinner,
  toast,
} from '@felinic/ui'
import { KeyRound } from 'lucide-vue-next'
import {
  type AcpprofilePublicProfile,
} from '@memohai/sdk'
import { useACPOAuth } from '@/composables/useACPOAuth'
import { useAcpSetupModeItems } from '@/composables/useAcpSetupModeItems'
import { getBotsQueryKey } from '@memohai/sdk/colada'
import {
  acpAgentIcon,
  ensureACPAgentForm,
  ensureHermesManagedDefaults,
  findMissingRequiredManagedField,
  isACPAgent,
  isClaudeCodeAgent,
  isCodexAgent,
  normalizeACPAgentID,
  type ACPAgentForm,
  type ACPForm,
} from '@/utils/acp'
import { filterSettingsVisibleManagedFields } from '@/utils/acp/setup-fields'
import AcpManagedFields from './acp-managed-fields.vue'

const props = defineProps<{
  botId: string
  profile: AcpprofilePublicProfile
  form: ACPForm
  pendingSelfConfirm?: boolean
}>()

interface ACPDetailCommitOptions {
  confirmSelf?: boolean
}

const emit = defineEmits<{
  commit: [options?: ACPDetailCommitOptions]
}>()

const { t } = useI18n()
const queryCache = useQueryCache()
const claudeOAuthCode = ref('')
const { setupModeItems } = useAcpSetupModeItems(() => props.profile)
const {
  codexStatus: codexOAuthStatus,
  codexStatusLoading: codexOAuthStatusLoading,
  authorizingCodexDevice,
  codexDeviceSession,
  codexDevicePending,
  claudeStatus: claudeOAuthStatus,
  claudeStatusLoading: claudeOAuthStatusLoading,
  authorizingClaude: authorizingClaudeOAuth,
  exchangingClaude: exchangingClaudeOAuth,
  claudeSessionId: claudeOAuthSessionId,
  loadCodexStatus: loadOAuthStatus,
  loadClaudeStatus: loadClaudeOAuthStatus,
  authorizeCodexDevice,
  cancelCodexDeviceAuthorization,
  clearCodexDeviceAuthorization,
  authorizeClaude,
  exchangeClaude,
} = useACPOAuth(() => props.botId)

const agent = computed<ACPAgentForm>(() => ensureACPAgentForm(props.form, props.profile))
const isGenericACP = computed(() => isACPAgent(props.profile.id))
const isCodex = computed(() => isCodexAgent(props.profile.id))
const isClaude = computed(() => isClaudeCodeAgent(props.profile.id))
const isHermes = computed(() => normalizeACPAgentID(props.profile.id) === 'hermes')
const isHermesSelfConfirmVisible = computed(() =>
  isHermes.value && props.pendingSelfConfirm === true && agent.value.enabled && agent.value.setup_mode === 'self',
)
const selfModeHint = computed(() => isHermes.value
  ? t('bots.settings.acpHermesSelfModeHint')
  : t('bots.settings.acpSelfModeHint'))

const visibleManagedFields = computed(() =>
  filterSettingsVisibleManagedFields(props.profile, agent.value.managed, agent.value.setup_mode),
)

// 只有走托管 OAuth 的两个 agent、且当前就在 OAuth 模式时,账号卡片才有存在意义。
const oauthSectionVisible = computed(() =>
  (isCodex.value || isClaude.value) && agent.value.setup_mode === 'oauth',
)

function commitForm() {
  if (agent.value.enabled && findMissingRequiredManagedField(props.profile, agent.value.managed, agent.value.setup_mode)) {
    return
  }
  emit('commit')
}

function confirmSelfMode() {
  if (!isHermesSelfConfirmVisible.value) return
  emit('commit', { confirmSelf: true })
}

function setSetupMode(mode: string) {
  agent.value.setup_mode = mode
  if (isHermes.value && mode === 'api_key') ensureHermesManagedDefaults(agent.value.managed)
  if (isCodex.value && mode === 'oauth') void loadOAuthStatus()
  if (isCodex.value && mode !== 'oauth') clearCodexDeviceAuthorization()
  if (isClaude.value && mode === 'oauth') void loadClaudeOAuthStatus()
  commitForm()
}

const codexOAuthActive = computed(() => isCodex.value && !!agent.value.enabled && agent.value.setup_mode === 'oauth')
const claudeOAuthActive = computed(() => isClaude.value && !!agent.value.enabled && agent.value.setup_mode === 'oauth')
const currentCodexDeviceSession = computed(() => {
  const session = codexDeviceSession.value
  return session?.bot_id === props.botId ? session : null
})

// 面板只在码还能用(或刚过期、可就地重取)时露出;error/cancelled 由行内状态和
// toast 交代,留一张废码在页面上只会误导。
const codexDevicePanelSession = computed(() => {
  const session = currentCodexDeviceSession.value
  if (!session || session.has_token) return null
  const usable = session.status === 'pending' || session.status === 'writing' || session.status === 'expired'
  return usable ? session : null
})

const claudeExchangeVisible = computed(() =>
  Boolean(claudeOAuthSessionId.value) && !claudeOAuthStatus.value?.has_token,
)

const codexButtonState = computed(() => {
  if (authorizingCodexDevice.value) return 'connecting'
  if (codexDevicePending.value) return 'cancel'
  return codexOAuthStatus.value?.has_token ? 'reconnect' : 'connect'
})

const claudeButtonState = computed(() => {
  if (authorizingClaudeOAuth.value) return 'connecting'
  return claudeOAuthStatus.value?.has_token ? 'reconnect' : 'connect'
})

// 行描述就是这张卡片的全部状态出口:失败/过期/等待/已连接/未连接各说一句。
const codexAccountDescription = computed(() => {
  const session = currentCodexDeviceSession.value
  if (session?.status === 'error') return session.error || t('bots.settings.acpCodexDeviceFailed')
  if (session?.status === 'expired') return t('bots.settings.acpCodexDeviceExpired')
  if (codexDevicePending.value) return t('provider.oauth.status.pendingDevice')
  if (codexOAuthStatus.value?.has_token) return t('provider.oauth.status.authorizedCurrent')
  if (codexOAuthStatusLoading.value) return t('provider.oauth.status.checking')
  return t('bots.settings.acpCodexConnectHint')
})

const claudeAccountDescription = computed(() => {
  if (claudeOAuthStatus.value?.has_token) return t('provider.oauth.status.authorizedCurrent')
  if (claudeExchangeVisible.value) return t('provider.oauth.status.oauthing')
  if (claudeOAuthStatusLoading.value) return t('provider.oauth.status.checking')
  return t('bots.settings.acpClaudeConnectHint')
})

watch([() => props.botId, () => props.profile.id, () => agent.value.setup_mode], () => {
  if (isHermes.value && agent.value.setup_mode === 'api_key') ensureHermesManagedDefaults(agent.value.managed)
  if (codexOAuthActive.value || (isCodex.value && agent.value.setup_mode === 'oauth')) void loadOAuthStatus()
  if (claudeOAuthActive.value || (isClaude.value && agent.value.setup_mode === 'oauth')) void loadClaudeOAuthStatus()
}, { immediate: true })

watch(claudeOAuthStatus, (status) => {
  if (status?.has_token) {
    agent.value.managed.oauth_token = agent.value.managed.oauth_token || '***'
  }
}, { immediate: true })

watch(() => codexDeviceSession.value?.status, (status, previousStatus) => {
  if (!status || status === previousStatus) return
  if (status === 'success') {
    markCodexOAuthAuthorized()
    toast.success(t('provider.oauth.authorizeSuccess'))
    return
  }
  if (status === 'expired') {
    toast.error(t('bots.settings.acpCodexDeviceExpired'))
    return
  }
  if (status === 'error') {
    toast.error(codexDeviceSession.value?.error || t('bots.settings.acpCodexDeviceFailed'))
  }
})

function invalidateOAuthQueries() {
  void queryCache.invalidateQueries({ key: ['bot', props.botId] })
  void queryCache.invalidateQueries({ key: getBotsQueryKey() })
}

function markCodexOAuthAuthorized() {
  agent.value.enabled = true
  agent.value.setup_mode = 'oauth'
  commitForm()
  invalidateOAuthQueries()
}

async function handleAuthorizeDevice() {
  if (!props.botId) return
  agent.value.setup_mode = 'oauth'
  const ok = await authorizeCodexDevice()
  if (!ok) {
    toast.error(t('provider.oauth.authorizeFailed'))
  }
}

async function handleCancelCodexDeviceAuthorization() {
  await cancelCodexDeviceAuthorization()
}

async function handleAuthorizeClaude() {
  agent.value.setup_mode = 'oauth'
  claudeOAuthCode.value = ''
  const ok = await authorizeClaude()
  if (!ok) toast.error(t('provider.oauth.authorizeFailed'))
}

async function handleExchangeClaudeOAuth() {
  const code = claudeOAuthCode.value.trim()
  if (!code) {
    toast.error(t('bots.settings.acpClaudeOAuthCodeRequired'))
    return
  }
  const ok = await exchangeClaude(code)
  if (ok) {
    agent.value.enabled = true
    agent.value.setup_mode = 'oauth'
    agent.value.managed.oauth_token = '***'
    claudeOAuthCode.value = ''
    commitForm()
    invalidateOAuthQueries()
    toast.success(t('provider.oauth.authorizeSuccess'))
  } else {
    toast.error(t('bots.settings.acpClaudeOAuthExchangeFailed'))
  }
}
</script>
