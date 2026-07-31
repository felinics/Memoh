<!-- eslint-disable vue/no-mutating-props -->
<template>
  <div class="space-y-8">
    <!-- Identity card: which agent this is. The brand mark is signal (which
         agent), not decoration. Mirrors the provider detail header so every
         backend reads the same way. -->
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

    <SettingsSection>
      <div class="space-y-5 p-4">
        <SegmentedControl
          :model-value="agent.setup_mode"
          :items="setupModeItems"
          :aria-label="$t('bots.settings.acpSetupMode')"
          class="w-full sm:w-fit"
          @update:model-value="(mode) => setSetupMode(String(mode))"
        />

        <template v-if="agent.setup_mode !== 'self'">
          <div
            v-if="isCodex && agent.setup_mode === 'oauth'"
            class="space-y-3"
          >
            <div
              class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between"
            >
              <p
                class="min-w-0 flex-1 text-sm"
                :class="codexOAuthTextClass()"
              >
                {{ codexOAuthStatusText() }}
              </p>
              <div class="flex shrink-0 flex-wrap items-center gap-2">
                <Button
                  v-if="codexDevicePending"
                  type="button"
                  variant="ghost"
                  @click="handleCancelCodexDeviceAuthorization"
                >
                  {{ $t('common.cancel') }}
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  :disabled="codexAuthorizing"
                  :loading="authorizingCodexOAuth"
                  @click="handleAuthorize"
                >
                  {{ $t('bots.settings.acpOAuthAuthorizeCodex') }}
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  :disabled="codexAuthorizing"
                  :loading="authorizingCodexDevice"
                  @click="handleAuthorizeDevice"
                >
                  {{ $t('bots.settings.acpCodexDeviceAuthorize') }}
                </Button>
              </div>
            </div>

            <div
              v-if="codexDevicePanelSession"
              class="space-y-3 rounded-md bg-accent p-3"
            >
              <p class="text-sm text-muted-foreground">
                {{ $t('bots.settings.acpCodexDeviceHint') }}
              </p>
              <div
                v-if="codexDeviceVerificationReady"
                class="space-y-1"
              >
                <div class="text-sm font-medium">
                  {{ $t('provider.oauth.deviceVerificationUri') }}
                </div>
                <code class="block break-all rounded-md bg-background px-2 py-1 text-sm select-all">{{ codexDevicePanelSession?.verification_url }}</code>
              </div>
              <div
                v-if="codexDeviceVerificationReady"
                class="space-y-1"
              >
                <div class="text-sm font-medium">
                  {{ $t('provider.oauth.deviceUserCode') }}
                </div>
                <div class="flex flex-col gap-2 sm:flex-row sm:items-center">
                  <code class="block min-w-0 flex-1 rounded-md bg-background px-2 py-1 font-mono text-sm select-all">{{ codexDevicePanelSession?.user_code }}</code>
                  <Button
                    type="button"
                    variant="outline"
                    class="shrink-0"
                    @click="handleOpenCodexDeviceVerification"
                  >
                    <Copy class="size-4" />
                    {{ $t('bots.settings.acpCodexDeviceCopyOpen') }}
                  </Button>
                </div>
              </div>
              <div
                v-if="codexDevicePanelSession?.expires_at"
                class="text-xs text-muted-foreground"
              >
                {{ $t('provider.oauth.deviceExpiresAt') }}: {{ codexDevicePanelSession.expires_at }}
              </div>
              <InlineLoadingRow
                v-if="codexDevicePending"
                size="md"
              >
                {{ $t('provider.oauth.status.pendingDevice') }}
              </InlineLoadingRow>
              <p
                v-else-if="codexDevicePanelSession?.status === 'error' && codexDevicePanelSession.error"
                class="text-sm text-destructive"
              >
                {{ codexDevicePanelSession.error }}
              </p>
              <p
                v-else-if="codexDevicePanelSession?.status === 'expired'"
                class="text-sm text-destructive"
              >
                {{ $t('bots.settings.acpCodexDeviceExpired') }}
              </p>
            </div>
          </div>

          <div
            v-if="isClaude && agent.setup_mode === 'oauth'"
            class="space-y-4"
          >
            <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
              <p
                class="min-w-0 flex-1 text-sm"
                :class="claudeOAuthTextClass()"
              >
                {{ claudeOAuthStatusText() }}
              </p>
              <Button
                type="button"
                variant="outline"
                class="shrink-0"
                :loading="authorizingClaudeOAuth"
                @click="handleAuthorizeClaude"
              >
                {{ $t('bots.settings.acpOAuthAuthorizeClaudeCode') }}
              </Button>
            </div>

            <div
              v-if="claudeOAuthSessionId && !claudeOAuthStatus?.has_token"
              class="space-y-2"
            >
              <p class="text-sm text-muted-foreground">
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
          </div>

          <FormStack>
            <FieldStack
              v-for="field in visibleManagedFields"
              :key="field.id"
              :label="field.label || field.id"
              :help="managedFieldHelp(field)"
            >
              <Select
                v-if="isHermesProviderField(field)"
                :model-value="hermesProvider"
                @update:model-value="(value) => setHermesProvider(String(value))"
              >
                <SelectTrigger class="w-full">
                  <SelectValue :placeholder="$t('bots.settings.acpHermesProviderPlaceholder')" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem
                    v-for="provider in HERMES_PROVIDER_PRESETS"
                    :key="provider.value"
                    :value="provider.value"
                  >
                    {{ $t(provider.labelKey) }}
                  </SelectItem>
                </SelectContent>
              </Select>
              <template v-else-if="isHermesModelField(field)">
                <Select
                  :model-value="hermesModelSelect"
                  @update:model-value="(value) => setHermesModel(String(value))"
                >
                  <SelectTrigger class="w-full">
                    <SelectValue :placeholder="$t('bots.settings.acpHermesModelPlaceholder')" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem
                      v-for="model in hermesModelOptions"
                      :key="model.value"
                      :value="model.value"
                    >
                      {{ model.label }}
                    </SelectItem>
                    <SelectItem :value="HERMES_CUSTOM_MODEL_VALUE">
                      {{ $t('bots.settings.acpHermesCustomModel') }}
                    </SelectItem>
                  </SelectContent>
                </Select>
                <Input
                  v-if="hermesUsingCustomModel"
                  class="mt-2"
                  :model-value="agent.managed.model || ''"
                  :name="managedFieldName(field)"
                  autocomplete="off"
                  autocapitalize="off"
                  autocorrect="off"
                  spellcheck="false"
                  :placeholder="$t('bots.settings.acpHermesCustomModelPlaceholder')"
                  @update:model-value="(val) => setManagedField(field.id, String(val ?? ''))"
                  @change="commitForm"
                />
              </template>
              <Input
                v-else
                :model-value="agent.managed[field.id || ''] || ''"
                :type="inputType(field.type)"
                :name="managedFieldName(field)"
                :autocomplete="managedFieldAutocomplete(field)"
                autocapitalize="off"
                autocorrect="off"
                spellcheck="false"
                :placeholder="managedFieldPlaceholder(field)"
                @update:model-value="(val) => setManagedField(field.id, String(val ?? ''))"
                @change="commitForm"
              />
            </FieldStack>
          </FormStack>
        </template>

        <p
          v-else
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
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { FieldStack, FormStack, InlineLoadingRow, SettingsSection, toast, useClipboard } from '@felinic/ui'
import { useQueryCache } from '@pinia/colada'
import {
  Button,
  Input,
  SegmentedControl,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  type SegmentedItem,
} from '@felinic/ui'
import { Copy } from 'lucide-vue-next'
import {
  type AcpprofileManagedField,
  type AcpprofilePublicProfile,
} from '@memohai/sdk'
import { useACPOAuth } from '@/composables/useACPOAuth'
import {
  HERMES_CUSTOM_MODEL_VALUE,
  HERMES_PROVIDER_PRESETS,
  acpAgentIcon,
  ensureACPAgentForm,
  ensureHermesManagedDefaults,
  findMissingRequiredManagedField,
  hermesAPIKeyPlaceholder,
  hermesDefaultModel,
  hermesModelPresets,
  hermesModelSelectValue,
  hermesProviderValue,
  isClaudeCodeAgent,
  isCodexAgent,
  isHermesCustomProvider,
  isHermesPresetModel,
  normalizeACPAgentID,
  type ACPAgentForm,
  type ACPForm,
} from '@/utils/acp'
import { oauthStatusTextKey } from '@/utils/oauth/status-text'

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
const { copyText } = useClipboard()
const claudeOAuthCode = ref('')
const {
  codexStatus: codexOAuthStatus,
  codexStatusLoading: codexOAuthStatusLoading,
  authorizingCodex: authorizingCodexOAuth,
  authorizingCodexDevice,
  codexAuthorizing,
  codexDeviceSession,
  codexDevicePending,
  codexDeviceVerificationReady,
  claudeStatus: claudeOAuthStatus,
  claudeStatusLoading: claudeOAuthStatusLoading,
  authorizingClaude: authorizingClaudeOAuth,
  exchangingClaude: exchangingClaudeOAuth,
  claudeSessionId: claudeOAuthSessionId,
  loadCodexStatus: loadOAuthStatus,
  loadClaudeStatus: loadClaudeOAuthStatus,
  authorizeCodex,
  authorizeCodexDevice,
  cancelCodexDeviceAuthorization,
  clearCodexDeviceAuthorization,
  openCodexDeviceVerification,
  authorizeClaude,
  exchangeClaude,
} = useACPOAuth(() => props.botId)

const agent = computed<ACPAgentForm>(() => ensureACPAgentForm(props.form, props.profile))
const isCodex = computed(() => isCodexAgent(props.profile.id))
const isClaude = computed(() => isClaudeCodeAgent(props.profile.id))
const isHermes = computed(() => normalizeACPAgentID(props.profile.id) === 'hermes')
const hermesProvider = computed(() => hermesProviderValue(agent.value.managed.provider))
const hermesModelOptions = computed(() => hermesModelPresets(hermesProvider.value))
const hermesModelSelect = computed(() => hermesModelSelectValue(hermesProvider.value, agent.value.managed.model))
const hermesUsingCustomModel = computed(() => hermesModelSelect.value === HERMES_CUSTOM_MODEL_VALUE)
const isHermesSelfConfirmVisible = computed(() =>
  isHermes.value && props.pendingSelfConfirm === true && agent.value.enabled && agent.value.setup_mode === 'self',
)
const selfModeHint = computed(() => isHermes.value
  ? t('bots.settings.acpHermesSelfModeHint')
  : t('bots.settings.acpSelfModeHint'))

function setupModes(): string[] {
  const modes = props.profile.setup_modes?.filter(Boolean) ?? []
  return modes.length > 0 ? modes : ['api_key']
}

function setupModeLabel(mode: string): string {
  if (mode === 'api_key') return t('bots.settings.acpSetupApiKey')
  if (mode === 'oauth') {
    if (isCodex.value) return t('bots.settings.acpSetupChatGPT')
    if (isClaude.value) return t('bots.settings.acpSetupClaude')
    return t('bots.settings.acpSetupOAuth')
  }
  if (mode === 'self') return t('bots.settings.acpSetupSelf')
  return mode
}

const setupModeItems = computed<SegmentedItem<string>[]>(() =>
  setupModes().map(mode => ({
    value: mode,
    label: setupModeLabel(mode),
  })),
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

function inputType(type: string | undefined): string {
  if (type === 'password') return 'password'
  if (type === 'url') return 'url'
  return 'text'
}

function managedFieldName(field: AcpprofileManagedField): string {
  return `acp-${normalizeACPAgentID(props.profile.id) || 'agent'}-${normalizeACPAgentID(field.id) || 'field'}`
}

function managedFieldAutocomplete(field: AcpprofileManagedField): string {
  return field.type === 'password' ? 'new-password' : 'off'
}

function managedFieldPlaceholder(field: AcpprofileManagedField): string | undefined {
  if (isHermes.value && normalizeACPAgentID(field.id) === 'api_key') {
    return hermesAPIKeyPlaceholder(hermesProvider.value, field.placeholder)
  }
  return field.placeholder
}

// Hermes provider/model fields render their own preset selects and never carry
// help text; every other managed field surfaces its schema-provided help.
function managedFieldHelp(field: AcpprofileManagedField): string {
  if (isHermesProviderField(field) || isHermesModelField(field)) return ''
  return field.help || ''
}

function setManagedField(fieldID: string | undefined, value: string) {
  const id = normalizeACPAgentID(fieldID)
  if (!id) return
  agent.value.managed[id] = value
}

function isHermesProviderField(field: AcpprofileManagedField): boolean {
  return isHermes.value && normalizeACPAgentID(field.id) === 'provider'
}

function isHermesModelField(field: AcpprofileManagedField): boolean {
  return isHermes.value && normalizeACPAgentID(field.id) === 'model'
}

function setHermesProvider(value: string) {
  const provider = hermesProviderValue(value)
  agent.value.managed.provider = provider
  agent.value.managed.model = hermesDefaultModel(provider)
  if (!isHermesCustomProvider(provider)) agent.value.managed.base_url = ''
  commitForm()
}

function setHermesModel(value: string) {
  if (value === HERMES_CUSTOM_MODEL_VALUE) {
    if (isHermesPresetModel(hermesProvider.value, agent.value.managed.model)) {
      agent.value.managed.model = ''
    }
  } else {
    agent.value.managed.model = value
  }
  commitForm()
}

const visibleManagedFields = computed<AcpprofileManagedField[]>(() => {
  const mode = agent.value.setup_mode
  return (props.profile.managed_fields ?? []).filter((field) => {
    const id = normalizeACPAgentID(field.id)
    if (id === 'provider_id') return false
    if (isHermes.value && id === 'base_url') return isHermesCustomProvider(hermesProvider.value)
    if (isCodex.value && mode === 'oauth') return false
    if (isClaude.value) {
      if (id === 'api_key') return mode === 'api_key'
      if (id === 'oauth_token') return false
    }
    return true
  })
})

const codexOAuthActive = computed(() => isCodex.value && !!agent.value.enabled && agent.value.setup_mode === 'oauth')
const claudeOAuthActive = computed(() => isClaude.value && !!agent.value.enabled && agent.value.setup_mode === 'oauth')
const currentCodexDeviceSession = computed(() => {
  const session = codexDeviceSession.value
  return session?.bot_id === props.botId ? session : null
})
const codexDevicePanelSession = computed(() => {
  const session = currentCodexDeviceSession.value
  if (!session || session.has_token || session.status === 'success') return null
  return session
})

const codexOAuthPending = computed(() => codexAuthorizing.value)
const claudeOAuthPending = computed(() => {
  if (authorizingClaudeOAuth.value || exchangingClaudeOAuth.value) return true
  return Boolean(claudeOAuthSessionId.value && !claudeOAuthStatus.value?.has_token)
})

// Codex/Claude in OAuth mode need their live token status the moment the detail
// opens (or the mode flips), so the status line and authorize button reflect reality.
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

function codexOAuthStatusText(): string {
  if (currentCodexDeviceSession.value?.status === 'expired') return t('bots.settings.acpCodexDeviceExpired')
  if (currentCodexDeviceSession.value?.status === 'error') return t('bots.settings.acpCodexDeviceFailed')
  if (codexDevicePending.value) return t('provider.oauth.status.pendingDevice')
  return t(oauthStatusTextKey({
    loading: codexOAuthStatusLoading.value,
    authorizing: authorizingCodexOAuth.value || authorizingCodexDevice.value,
    status: codexOAuthStatus.value,
    unavailableKey: 'bots.settings.acpOAuthUnavailable',
  }))
}

function codexOAuthTextClass(): string {
  return codexOAuthStatusLoading.value || codexOAuthPending.value || codexOAuthStatus.value?.has_token
    ? 'text-muted-foreground'
    : 'text-destructive'
}

function claudeOAuthStatusText(): string {
  return t(oauthStatusTextKey({
    loading: claudeOAuthStatusLoading.value,
    authorizing: claudeOAuthPending.value,
    status: claudeOAuthStatus.value,
    unavailableKey: 'bots.settings.acpClaudeOAuthUnavailable',
  }))
}

function claudeOAuthTextClass(): string {
  return claudeOAuthStatusLoading.value || claudeOAuthPending.value || claudeOAuthStatus.value?.has_token
    ? 'text-muted-foreground'
    : 'text-destructive'
}

function invalidateOAuthQueries() {
  void queryCache.invalidateQueries({ key: ['bot', props.botId] })
  void queryCache.invalidateQueries({ key: ['bots'] })
}

function markCodexOAuthAuthorized() {
  agent.value.enabled = true
  agent.value.setup_mode = 'oauth'
  commitForm()
  invalidateOAuthQueries()
}

async function handleAuthorize() {
  if (!props.botId) return
  const botId = props.botId
  agent.value.setup_mode = 'oauth'
  const ok = await authorizeCodex({ timeoutMs: 300_000 })
  if (ok && botId === props.botId) {
    markCodexOAuthAuthorized()
    toast.success(t('provider.oauth.authorizeSuccess'))
  } else if (botId === props.botId) {
    toast.error(t('provider.oauth.authorizeFailed'))
  }
}

async function handleAuthorizeDevice() {
  if (!props.botId) return
  agent.value.setup_mode = 'oauth'
  const ok = await authorizeCodexDevice()
  if (!ok) {
    toast.error(t('provider.oauth.authorizeFailed'))
  }
}

async function handleOpenCodexDeviceVerification() {
  const result = await openCodexDeviceVerification(copyText)
  if (result === 'opened') {
    toast.success(t('common.copied'))
  } else if (result === 'popup_blocked') {
    toast.error(t('bots.settings.acpCodexDevicePopupBlocked'))
  } else {
    toast.error(t('provider.oauth.copyFailed'))
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
