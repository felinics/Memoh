<template>
  <div class="space-y-8">
    <!-- Identity card: the platform this connection belongs to, with the
         committing actions (enable/disable, save) on the right. -->
    <div class="flex items-center gap-3 rounded-[var(--radius-menu-shell)] border border-border bg-card p-4">
      <span class="flex size-11 shrink-0 items-center justify-center rounded-full bg-muted">
        <ChannelIcon
          :channel="platformType"
          size="1.5em"
        />
      </span>
      <div class="min-w-0 flex-1">
        <h2 class="truncate text-sm font-semibold text-foreground">
          {{ channelTitle }}
        </h2>
        <p
          v-if="isEditMode"
          class="mt-0.5 flex items-center gap-1.5 text-xs"
          :class="form.disabled ? 'text-muted-foreground' : 'text-success'"
        >
          <span class="size-1.5 rounded-full bg-current" />
          {{ form.disabled ? $t('bots.channels.statusInactive') : $t('bots.channels.statusActive') }}
        </p>
      </div>
      <div class="flex shrink-0 items-center gap-2">
        <span
          v-if="isFormDirty"
          class="hidden text-xs text-muted-foreground sm:inline"
        >
          {{ $t('common.unsaved') }}
        </span>
        <Button
          v-if="isEditMode"
          variant="outline"
          size="sm"
          :disabled="isBusy"
          :loading="action === 'toggle'"
          @click="handleToggleDisabled"
        >
          {{ form.disabled ? $t('bots.channels.actionEnable') : $t('bots.channels.actionDisable') }}
        </Button>
        <Button
          size="sm"
          :disabled="(!isFormDirty && isEditMode) || isBusy"
          :loading="action === 'save'"
          @click="handleSave"
        >
          {{ action === 'save' ? $t('bots.channels.verifying') : $t('bots.settings.save') }}
        </Button>
      </div>
    </div>

    <!-- WeChat pairs by scanning a QR rather than entering credentials. -->
    <div v-if="channelItem.meta.type === 'weixin'">
      <WeixinQrLogin
        :bot-id="botId"
        @login-success="handleWeixinLoginSuccess"
      />
    </div>

    <template v-else>
      <!-- Callback URL the platform console needs (Feishu webhook mode / WeChat OA) -->
      <SettingsSection
        v-if="showWebhookCallback"
        :title="$t('bots.channels.webhookCallback')"
      >
        <div class="mx-4 space-y-3 py-4">
          <p class="text-xs text-muted-foreground">
            {{ $t(webhookCallbackHintKey) }}
          </p>
          <p
            v-if="lineWebhookBaseWarningKey"
            class="text-xs text-warning"
          >
            {{ $t(lineWebhookBaseWarningKey) }}
          </p>
          <div
            v-if="webhookCallbackUrl"
            class="flex flex-col gap-2 sm:flex-row sm:items-center"
          >
            <Input
              :model-value="webhookCallbackUrl"
              readonly
              class="font-mono sm:flex-1"
            />
            <div class="flex shrink-0 items-center gap-2">
              <Button
                variant="outline"
                class="shrink-0"
                @click="copyWebhookCallback"
              >
                {{ $t('common.copy') }}
              </Button>
              <Button
                v-if="isLineWebhook"
                variant="outline"
                class="shrink-0"
                :disabled="isBusy"
                :loading="action === 'webhook'"
                @click="handleSetLineWebhookEndpoint"
              >
                {{ action === 'webhook' ? $t('bots.channels.lineWebhookSetting') : $t('bots.channels.lineWebhookSet') }}
              </Button>
            </div>
          </div>
          <p
            v-else
            class="text-xs italic text-muted-foreground"
          >
            {{ $t(webhookCallbackPendingKey) }}
          </p>
          <p
            v-if="isLineWebhook"
            class="text-xs text-muted-foreground"
          >
            {{ $t('bots.channels.linePublicMediaLimit') }}
          </p>
        </div>
      </SettingsSection>

      <!-- Credentials + optional parameters; optional fields collapse behind one toggle -->
      <SettingsSection
        v-if="requiredFieldsKeys.length > 0 || optionalFieldsKeys.length > 0"
        :title="$t('bots.channels.credentials')"
      >
        <p
          v-if="isFeishuWebhook"
          class="mx-4 border-b border-border py-3 text-xs text-warning"
        >
          {{ $t('bots.channels.feishuWebhookSecurityHint') }}
        </p>

        <ChannelField
          v-for="key in requiredFieldsKeys"
          :key="key"
          v-model="form.credentials[key]"
          :field="orderedFields[key]"
          :field-key="key"
        />
      </SettingsSection>

      <!-- Optional parameters behind a named ActionCard entry opening a
           focused dialog — the house replacement for the old in-card
           "Expand all" row. Slim single-line entry per the ActionCard
           contract. -->
      <ActionCard
        v-if="optionalFieldsKeys.length > 0"
        :title="$t('bots.channels.advancedTitle')"
        @click="isAdvancedExpanded = true"
      >
        <template #icon>
          <SlidersHorizontal />
        </template>
      </ActionCard>

      <Dialog v-model:open="isAdvancedExpanded">
        <DialogPanel>
          <DialogHeader>
            <DialogTitle>{{ $t('bots.channels.advancedTitle') }}</DialogTitle>
          </DialogHeader>
          <DialogBody>
            <ChannelField
              v-for="key in optionalFieldsKeys"
              :key="key"
              v-model="form.credentials[key]"
              :field="orderedFields[key]"
              :field-key="key"
            />
          </DialogBody>
        </DialogPanel>
      </Dialog>
    </template>

    <!-- Removing a connection is irreversible, so it sits in its own card -->
    <SettingsSection
      v-if="isEditMode"
      :title="$t('bots.channels.dangerZone')"
    >
      <SettingsRow
        :label="$t('common.delete')"
        :description="$t('bots.channels.deleteWarning')"
      >
        <ConfirmPopover
          :title="$t('bots.channels.deleteTitle')"
          :message="$t('bots.channels.deleteConfirm')"
          :cancel-text="$t('common.cancel')"
          :confirm-text="$t('common.delete')"
          variant="destructive"
          :loading="action === 'delete'"
          @confirm="handleDelete"
        >
          <template #trigger>
            <Button
              variant="destructive"
              size="sm"
              :disabled="isBusy"
              :loading="action === 'delete'"
            >
              {{ $t('common.delete') }}
            </Button>
          </template>
        </ConfirmPopover>
      </SettingsRow>
    </SettingsSection>
  </div>
</template>

<script setup lang="ts">
import { ActionCard, Button, ConfirmPopover, Dialog, DialogBody, DialogHeader, DialogPanel, DialogTitle, Input, SettingsRow, SettingsSection, useClipboard } from '@felinic/ui'
import { SlidersHorizontal } from 'lucide-vue-next'
import { reactive, watch, computed, ref } from 'vue'
import { toast } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import { useMutation } from '@pinia/colada'
import { putBotsByIdChannelByPlatform, deleteBotsByIdChannelByPlatform, patchBotsByIdChannelByPlatformStatus, postBotsByIdChannelByPlatformWebhookEndpoint } from '@memohai/sdk'
import type { HandlersChannelMeta, ChannelChannelConfig, ChannelFieldSchema, ChannelUpsertConfigRequest } from '@memohai/sdk'
import { client } from '@memohai/sdk/client'
import ChannelIcon from '@/components/channel-icon/index.vue'
import ChannelField from './channel-field.vue'
import WeixinQrLogin from './weixin-qr-login.vue'
import { channelTypeDisplayName } from '@/utils/channel-type-label'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { useLineWebhookPublicBase } from '../composables/use-line-webhook-public-base'

export interface BotChannelItem {
  meta: HandlersChannelMeta
  config: ChannelChannelConfig | null
  configured: boolean
}

const props = defineProps<{
  botId: string
  channelItem: BotChannelItem
}>()

const emit = defineEmits<{
  saved: []
  deleted: []
  'update:dirty': [isDirty: boolean]
}>()

const { t } = useI18n()
const { copyText } = useClipboard()
const botIdRef = computed(() => props.botId)
const platformType = computed(() => String(props.channelItem.meta.type || '').trim())
const channelTitle = computed(() => channelTypeDisplayName(t, props.channelItem.meta.type, props.channelItem.meta.display_name))

const action = ref<'save' | 'toggle' | 'delete' | 'webhook' | ''>('')
const isBusy = computed(() => action.value !== '')
const isEditMode = computed(() => props.channelItem.configured)
const lastSavedConfigId = ref('')

const form = reactive<{ credentials: Record<string, unknown>; disabled: boolean }>({ credentials: {}, disabled: false })
const initialCredentialsString = ref('')
const isAdvancedExpanded = ref(false)

const { mutateAsync: upsertChannel } = useMutation({
  mutation: async ({ platform, data }: { platform: string; data: ChannelUpsertConfigRequest }) => {
    const { data: result } = await putBotsByIdChannelByPlatform({ path: { id: botIdRef.value, platform }, body: data, throwOnError: true })
    return result
  }
})
const { mutateAsync: updateChannelStatus } = useMutation({
  mutation: async ({ platform, disabled }: { platform: string; disabled: boolean }) => {
    const { data } = await patchBotsByIdChannelByPlatformStatus({ path: { id: botIdRef.value, platform }, body: { disabled }, throwOnError: true })
    return data
  }
})
const { mutateAsync: setWebhookEndpoint } = useMutation({
  mutation: async ({ platform, endpoint }: { platform: string; endpoint: string }) => {
    const { data } = await postBotsByIdChannelByPlatformWebhookEndpoint({ path: { id: botIdRef.value, platform }, body: { endpoint }, throwOnError: true })
    return data
  }
})

const orderedFields = computed(() => {
  const fields = props.channelItem.meta.config_schema?.fields ?? {}
  const entries = Object.entries(fields).filter(([key]) => key !== 'status' && key !== 'disabled')
  entries.sort(([keyA, a], [keyB, b]) => {
    if (a.required && !b.required) return -1
    if (!a.required && b.required) return 1
    const orderA = a.order ?? Number.MAX_SAFE_INTEGER
    const orderB = b.order ?? Number.MAX_SAFE_INTEGER
    return orderA !== orderB ? orderA - orderB : keyA.localeCompare(keyB)
  })
  return Object.fromEntries(entries) as Record<string, ChannelFieldSchema>
})

const requiredFieldsKeys = computed(() => Object.keys(orderedFields.value).filter(k => orderedFields.value[k].required))
const optionalFieldsKeys = computed(() => Object.keys(orderedFields.value).filter(k => !orderedFields.value[k].required))

const currentInboundMode = computed(() => String(form.credentials.inboundMode ?? form.credentials.inbound_mode ?? '').trim().toLowerCase())
const isFeishuWebhook = computed(() => platformType.value === 'feishu' && currentInboundMode.value === 'webhook')
const isWechatOA = computed(() => platformType.value === 'wechatoa')
const isLineWebhook = computed(() => platformType.value === 'line')
const { publicBase: lineWebhookPublicBase, warningKey: lineWebhookBaseWarningKey } = useLineWebhookPublicBase(isLineWebhook)
const showWebhookCallback = computed(() => isFeishuWebhook.value || isWechatOA.value || isLineWebhook.value)
const webhookCallbackHintKey = computed(() => {
  if (isLineWebhook.value) return 'bots.channels.lineWebhookCallbackHint'
  if (isWechatOA.value) return 'bots.channels.wechatOAWebhookCallbackHint'
  return 'bots.channels.webhookCallbackHint'
})
const webhookConfigId = computed(() => String(props.channelItem.config?.id || lastSavedConfigId.value || '').trim())
const webhookCallbackPendingKey = computed(() => {
  if (isLineWebhook.value && webhookConfigId.value && !lineWebhookPublicBase.value.url) {
    return 'bots.channels.webhookCallbackPublicBasePending'
  }
  return 'bots.channels.webhookCallbackPending'
})
const webhookCallbackUrl = computed(() => {
  if (!showWebhookCallback.value) return ''
  return webhookConfigId.value ? buildWebhookCallbackUrl(webhookConfigId.value) : ''
})

function initForm() {
  const schema = props.channelItem.meta.config_schema?.fields ?? {}
  const existingCredentials = props.channelItem.config?.credentials ?? {}
  const creds: Record<string, unknown> = {}

  for (const [key, field] of Object.entries(schema)) {
    if (existingCredentials[key] !== undefined) {
      creds[key] = existingCredentials[key]
    } else {
      creds[key] = field.type === 'bool' ? undefined : ''
    }
  }
  form.credentials = creds
  form.disabled = props.channelItem.config?.disabled ?? false
  lastSavedConfigId.value = String(props.channelItem.config?.id || '').trim()
  initialCredentialsString.value = JSON.stringify(creds)

  // NOTE: the old inline expand auto-opened when an optional field was already
  // filled, so configured values never sat hidden. The optional surface is now
  // a DIALOG — auto-popping a modal on init would be hostile; the
  // always-visible ActionCard entry keeps the facet discoverable.
  isAdvancedExpanded.value = false
  emit('update:dirty', false)
}

watch(() => props.channelItem, initForm, { immediate: true })

// Stringify the reactive proxy (not toRaw) so the computed actually tracks nested
// credential edits — otherwise Save never re-enables after a field changes.
const isFormDirty = computed(() => JSON.stringify(form.credentials) !== initialCredentialsString.value)
watch(isFormDirty, (val) => emit('update:dirty', val))

function validateRequired(): boolean {
  for (const key of requiredFieldsKeys.value) {
    const val = form.credentials[key]
    if (!val || (typeof val === 'string' && val.trim() === '')) {
      toast.error(t('bots.channels.requiredField', { field: orderedFields.value[key].title || key }))
      return false
    }
  }
  if (platformType.value === 'feishu' && currentInboundMode.value === 'webhook') {
    if (!String(form.credentials.encryptKey || form.credentials.encrypt_key || '').trim() && !String(form.credentials.verificationToken || form.credentials.verification_token || '').trim()) {
      toast.error(t('bots.channels.feishuWebhookSecretRequired'))
      return false
    }
  }
  return true
}

async function handleSave() {
  if (!validateRequired()) return
  action.value = 'save'
  try {
    const cleanCreds = Object.fromEntries(Object.entries(form.credentials).filter(([k, v]) => k !== 'status' && k !== 'disabled' && v !== '' && v !== undefined && v !== null))
    const result = await upsertChannel({ platform: platformType.value, data: { credentials: cleanCreds, disabled: form.disabled } })
    lastSavedConfigId.value = String(result?.id || lastSavedConfigId.value || '').trim()
    initialCredentialsString.value = JSON.stringify(form.credentials)
    toast.success(t('bots.channels.saveSuccess'))
    emit('update:dirty', false)
    emit('saved')
  } catch (err) {
    toast.error(resolveApiErrorMessage(err, t('bots.channels.saveFailed'), { prefixFallback: true }))
  } finally {
    action.value = ''
  }
}

async function handleToggleDisabled() {
  action.value = 'toggle'
  try {
    const result = await updateChannelStatus({ platform: platformType.value, disabled: !form.disabled })
    form.disabled = !!result?.disabled
    toast.success(t('bots.channels.saveSuccess'))
    emit('saved')
  } catch (err) {
    toast.error(resolveApiErrorMessage(err, t('bots.channels.saveFailed'), { prefixFallback: true }))
  } finally {
    action.value = ''
  }
}

async function handleDelete() {
  action.value = 'delete'
  try {
    await deleteBotsByIdChannelByPlatform({ path: { id: botIdRef.value, platform: platformType.value }, throwOnError: true })
    lastSavedConfigId.value = ''
    toast.success(t('bots.channels.deleteSuccess'))
    emit('deleted')
  } catch (err) {
    toast.error(resolveApiErrorMessage(err, t('bots.channels.deleteFailed'), { prefixFallback: true }))
  } finally {
    action.value = ''
  }
}

function buildWebhookCallbackUrl(configId: string): string {
  if (isLineWebhook.value) {
    const base = lineWebhookPublicBase.value.url
    return base ? `${base}/channels/line/webhook/${encodeURIComponent(configId)}` : ''
  }
  const base = (import.meta.env.VITE_WEBHOOK_PUBLIC_BASE_URL?.trim() || import.meta.env.VITE_API_PUBLIC_URL?.trim() || client.getConfig().baseUrl || import.meta.env.VITE_API_URL?.trim() || (typeof window !== 'undefined' ? new URL(window.location.origin).toString() : '')).replace(/\/+$/, '')
  return `${base}/channels/${encodeURIComponent(platformType.value)}/webhook/${encodeURIComponent(configId)}`
}

async function copyWebhookCallback() {
  if (!webhookCallbackUrl.value) {
    toast.error(t('bots.channels.copyFailed'))
    return
  }
  const ok = await copyText(webhookCallbackUrl.value)
  if (!ok) {
    toast.error(t('bots.channels.copyFailed'))
    return
  }
  toast.success(t('common.copied'))
}

async function handleSetLineWebhookEndpoint() {
  if (!webhookCallbackUrl.value) return
  if (isFormDirty.value) {
    toast.error(t('bots.channels.lineWebhookSaveFirst'))
    return
  }
  action.value = 'webhook'
  try {
    await setWebhookEndpoint({ platform: platformType.value, endpoint: webhookCallbackUrl.value })
    toast.success(t('bots.channels.lineWebhookSetSuccess'))
  } catch (err) {
    toast.error(resolveApiErrorMessage(err, t('bots.channels.lineWebhookSetFailed'), { prefixFallback: true }))
  } finally {
    action.value = ''
  }
}

function handleWeixinLoginSuccess() { emit('saved') }
</script>
