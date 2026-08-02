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
          :field="getOrderedField(key)"
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
              :field="getOrderedField(key)"
              :field-key="key"
            />
          </DialogBody>
        </DialogPanel>
      </Dialog>

      <SettingsSection
        v-if="isTelegram"
        :title="$t('bots.channels.telegramDiscussTitle')"
      >
        <SettingsRow
          :label="$t('bots.channels.telegramPassiveRate')"
          :description="$t('bots.channels.telegramPassiveRateDescription')"
        >
          <div class="flex w-48 items-center gap-3">
            <Slider
              :model-value="[telegramPassiveRate]"
              :min="0"
              :max="1"
              :step="0.01"
              :disabled="!telegramPolicyLoaded || isBusy"
              class="min-w-0 flex-1"
              @update:model-value="updateTelegramPassiveRate"
            />
            <span class="w-10 text-right text-xs tabular-nums text-muted-foreground">
              {{ Math.round(telegramPassiveRate * 100) }}%
            </span>
          </div>
        </SettingsRow>
        <SettingsRow
          :label="$t('bots.channels.telegramForceReplyKeywords')"
          :description="$t('bots.channels.telegramForceReplyKeywordsDescription')"
        >
          <TagsInput
            :model-value="telegramForceReplyKeywords"
            :add-on-blur="true"
            :disabled="!telegramPolicyLoaded || isBusy"
            class="w-72"
            @update:model-value="(values) => telegramForceReplyKeywords = normalizeTelegramKeywords(values.map(String))"
          >
            <TagsInputItem
              v-for="keyword in telegramForceReplyKeywords"
              :key="keyword.toLocaleLowerCase()"
              :value="keyword"
            >
              <TagsInputItemText />
              <TagsInputItemDelete />
            </TagsInputItem>
            <TagsInputInput :placeholder="$t('bots.channels.telegramForceReplyKeywordsPlaceholder')" />
          </TagsInput>
        </SettingsRow>
        <SettingsRow
          :label="$t('bots.channels.telegramSendFallbackEnabled')"
          :description="$t('bots.channels.telegramSendFallbackEnabledDescription')"
        >
          <Switch
            :model-value="telegramSendFallbackEnabled"
            :disabled="!telegramPolicyLoaded || isBusy"
            :aria-label="$t('bots.channels.telegramSendFallbackEnabled')"
            @update:model-value="(enabled) => telegramSendFallbackEnabled = !!enabled"
          />
        </SettingsRow>
      </SettingsSection>

      <SettingsSection
        v-if="isTelegram"
        :title="$t('bots.channels.telegramContextTitle')"
      >
        <SettingsRow
          :label="$t('bots.channels.telegramMessageMetadata')"
          :description="$t('bots.channels.telegramMessageMetadataDescription')"
        >
          <Select
            :model-value="telegramMessageMetadataMode"
            :disabled="!telegramPolicyLoaded || isBusy"
            @update:model-value="(value) => telegramMessageMetadataMode = normalizeTelegramMessageMetadataMode(value)"
          >
            <SelectTrigger class="w-48">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="compact">
                {{ $t('bots.channels.telegramMessageMetadataCompact') }}
              </SelectItem>
              <SelectItem value="full">
                {{ $t('bots.channels.telegramMessageMetadataFull') }}
              </SelectItem>
            </SelectContent>
          </Select>
        </SettingsRow>
      </SettingsSection>

      <SettingsSection
        v-if="isTelegram"
        :title="$t('bots.channels.telegramToolsTitle')"
      >
        <SettingsRow
          :label="$t('bots.channels.telegramToolCallsEnabled')"
          :description="$t('bots.channels.telegramToolCallsEnabledDescription')"
        >
          <Switch
            :model-value="telegramToolCallsEnabled"
            :disabled="!telegramPolicyLoaded || isBusy"
            :aria-label="$t('bots.channels.telegramToolCallsEnabled')"
            @update:model-value="(enabled) => telegramToolCallsEnabled = !!enabled"
          />
        </SettingsRow>
        <SettingsRow
          :label="$t('bots.channels.telegramSkillsEnabled')"
          :description="$t('bots.channels.telegramSkillsEnabledDescription')"
        >
          <Switch
            :model-value="telegramSkillsEnabled"
            :disabled="!telegramPolicyLoaded || isBusy || !telegramToolCallsEnabled"
            :aria-label="$t('bots.channels.telegramSkillsEnabled')"
            @update:model-value="(enabled) => telegramSkillsEnabled = !!enabled"
          />
        </SettingsRow>
        <SettingsRow
          :label="$t('bots.channels.telegramToolsBulk')"
          :description="$t('bots.channels.telegramToolsDescription')"
        >
          <div class="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              :disabled="telegramToolCatalogLoading || isBusy || !telegramToolCallsEnabled || telegramTools.length === 0"
              @click="setAllTelegramTools(true)"
            >
              {{ $t('bots.channels.telegramToolsEnableAll') }}
            </Button>
            <Button
              variant="outline"
              size="sm"
              :disabled="telegramToolCatalogLoading || isBusy || !telegramToolCallsEnabled || telegramTools.length === 0"
              @click="setAllTelegramTools(false)"
            >
              {{ $t('bots.channels.telegramToolsDisableAll') }}
            </Button>
          </div>
        </SettingsRow>
        <InlineLoadingRow
          v-if="telegramToolCatalogLoading"
          surface="card-row"
        >
          {{ $t('common.loading') }}
        </InlineLoadingRow>
        <SettingsRow
          v-else-if="telegramToolCatalogError"
          :label="$t('errors.tool.catalog_unavailable')"
        />
        <SettingsRow
          v-else-if="telegramTools.length === 0"
          :label="$t('bots.channels.telegramToolsEmpty')"
          :description="$t('bots.channels.telegramToolsEmptyDescription')"
        />
        <template v-else>
          <SettingsRow
            v-for="tool in telegramTools"
            :key="tool.name"
            :label="tool.name"
            :description="tool.description"
          >
            <Switch
              :model-value="isTelegramToolEnabled(tool.name)"
              :disabled="!telegramPolicyLoaded || isBusy || !telegramToolCallsEnabled"
              :aria-label="tool.name"
              @update:model-value="(enabled) => setTelegramToolEnabled(tool.name, !!enabled)"
            />
          </SettingsRow>
        </template>
        <p
          v-if="telegramPolicyLoaded && telegramToolCallsEnabled && !isTelegramToolEnabled('send')"
          class="mx-4 py-2 text-xs text-warning"
        >
          {{ $t('bots.channels.telegramSendDisabledWarning') }}
        </p>
      </SettingsSection>

      <TelegramStickerCatalog
        v-if="isTelegram"
        :bot-id="botId"
      />
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
import {
  ActionCard, Button, Dialog, DialogBody, DialogHeader, DialogPanel, DialogTitle, InlineLoadingRow, Input,
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue, Slider, Switch,
  TagsInput, TagsInputInput, TagsInputItem, TagsInputItemDelete, TagsInputItemText,
} from '@felinic/ui'
import { SlidersHorizontal } from 'lucide-vue-next'
import { reactive, watch, computed, ref } from 'vue'
import { toast } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import { useMutation, useQuery, useQueryCache } from '@pinia/colada'
import { getBotsByBotIdToolsCatalog, getBotsById, putBotsById, putBotsByIdChannelByPlatform, deleteBotsByIdChannelByPlatform, patchBotsByIdChannelByPlatformStatus, postBotsByIdChannelByPlatformWebhookEndpoint } from '@memohai/sdk'
import type { HandlersChannelMeta, HandlersToolCatalogItem, ChannelChannelConfig, ChannelFieldSchema, ChannelUpsertConfigRequest } from '@memohai/sdk'
import { client } from '@memohai/sdk/client'
import ConfirmPopover from '@/components/confirm-popover/index.vue'
import ChannelIcon from '@/components/channel-icon/index.vue'
import SettingsSection from '@/components/settings/section.vue'
import SettingsRow from '@/components/settings/row.vue'
import ChannelField from './channel-field.vue'
import TelegramStickerCatalog from './telegram-sticker-catalog.vue'
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
const botIdRef = computed(() => props.botId)
const platformType = computed(() => String(props.channelItem.meta.type || '').trim())
const channelTitle = computed(() => channelTypeDisplayName(t, props.channelItem.meta.type, props.channelItem.meta.display_name))
const isTelegram = computed(() => platformType.value === 'telegram')
const queryCache = useQueryCache()

const TELEGRAM_PASSIVE_RATE_KEY = 'telegram_discuss_passive_sample_rate'
const TELEGRAM_FORCE_REPLY_KEYWORDS_KEY = 'telegram_discuss_force_reply_keywords'
const TELEGRAM_SEND_FALLBACK_ENABLED_KEY = 'telegram_discuss_send_fallback_enabled'
const TELEGRAM_TOOL_CALLS_ENABLED_KEY = 'telegram_tool_calls_enabled'
const TELEGRAM_ENABLED_TOOLS_KEY = 'telegram_enabled_tools'
const TELEGRAM_SKILLS_ENABLED_KEY = 'telegram_skills_enabled'
const TELEGRAM_MESSAGE_METADATA_MODE_KEY = 'telegram_message_metadata_mode'
const DEFAULT_TELEGRAM_PASSIVE_RATE = 0.25
const DEFAULT_TELEGRAM_MESSAGE_METADATA_MODE = 'compact'
const EMPTY_CHANNEL_FIELD: ChannelFieldSchema = {}

const { data: bot } = useQuery({
  key: () => ['bot', botIdRef.value],
  query: async () => {
    const { data } = await getBotsById({ path: { id: botIdRef.value }, throwOnError: true })
    return data
  },
  enabled: () => isTelegram.value && !!botIdRef.value,
})

const { data: telegramToolCatalog, error: telegramToolCatalogError, isLoading: telegramToolCatalogLoading } = useQuery({
  key: () => ['bot-tool-catalog', botIdRef.value, 'telegram'],
  query: async () => {
    const { data } = await getBotsByBotIdToolsCatalog({
      path: { bot_id: botIdRef.value },
      query: { platform: 'telegram' },
      throwOnError: true,
    })
    return data
  },
  enabled: () => isTelegram.value && !!botIdRef.value,
})

const action = ref<'save' | 'toggle' | 'delete' | 'webhook' | ''>('')
const isBusy = computed(() => action.value !== '')
const isEditMode = computed(() => props.channelItem.configured)
const lastSavedConfigId = ref('')

const form = reactive<{ credentials: Record<string, unknown>; disabled: boolean }>({ credentials: {}, disabled: false })
const initialCredentialsString = ref('')
const isAdvancedExpanded = ref(false)
const telegramPassiveRate = ref(DEFAULT_TELEGRAM_PASSIVE_RATE)
const telegramForceReplyKeywords = ref<string[]>([])
const savedTelegramPassiveRate = ref(DEFAULT_TELEGRAM_PASSIVE_RATE)
const savedTelegramForceReplyKeywords = ref<string[]>([])
const telegramSendFallbackEnabled = ref(false)
const savedTelegramSendFallbackEnabled = ref(false)
const telegramEnabledTools = ref<string[]>([])
const savedTelegramEnabledTools = ref<string[]>([])
const telegramToolCallsEnabled = ref(true)
const savedTelegramToolCallsEnabled = ref(true)
const telegramSkillsEnabled = ref(true)
const savedTelegramSkillsEnabled = ref(true)
const telegramToolPolicyConfigured = ref(false)
const savedTelegramToolPolicyConfigured = ref(false)
const telegramMessageMetadataMode = ref(DEFAULT_TELEGRAM_MESSAGE_METADATA_MODE)
const savedTelegramMessageMetadataMode = ref(DEFAULT_TELEGRAM_MESSAGE_METADATA_MODE)
const telegramPolicyLoaded = ref(false)

const telegramTools = computed<Array<Required<Pick<HandlersToolCatalogItem, 'name'>> & HandlersToolCatalogItem>>(() => {
  const seen = new Set<string>()
  const tools: Array<Required<Pick<HandlersToolCatalogItem, 'name'>> & HandlersToolCatalogItem> = []
  for (const item of telegramToolCatalog.value?.tools ?? []) {
    const name = String(item.name || '').trim()
    if (!name || name === 'list_skills' || name === 'use_skill' || seen.has(name)) continue
    seen.add(name)
    tools.push({ ...item, name })
  }
  return tools
})

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

const requiredFieldsKeys = computed(() => Object.keys(orderedFields.value).filter(k => orderedFields.value[k]?.required))
const optionalFieldsKeys = computed(() => Object.keys(orderedFields.value).filter(k => !orderedFields.value[k]?.required))

function getOrderedField(key: string): ChannelFieldSchema {
  return orderedFields.value[key] ?? EMPTY_CHANNEL_FIELD
}

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
}

watch(() => props.channelItem, initForm, { immediate: true })

// Stringify the reactive proxy (not toRaw) so the computed actually tracks nested
// credential edits — otherwise Save never re-enables after a field changes.
const isChannelFormDirty = computed(() => JSON.stringify(form.credentials) !== initialCredentialsString.value)
const isTelegramPolicyDirty = computed(() => isTelegram.value && telegramPolicyLoaded.value && (
  telegramPassiveRate.value !== savedTelegramPassiveRate.value
  || JSON.stringify(telegramForceReplyKeywords.value) !== JSON.stringify(savedTelegramForceReplyKeywords.value)
  || telegramSendFallbackEnabled.value !== savedTelegramSendFallbackEnabled.value
  || telegramMessageMetadataMode.value !== savedTelegramMessageMetadataMode.value
  || telegramToolCallsEnabled.value !== savedTelegramToolCallsEnabled.value
  || telegramSkillsEnabled.value !== savedTelegramSkillsEnabled.value
  || telegramToolPolicyConfigured.value !== savedTelegramToolPolicyConfigured.value
  || JSON.stringify(telegramEnabledTools.value) !== JSON.stringify(savedTelegramEnabledTools.value)
))
const isFormDirty = computed(() => isChannelFormDirty.value || isTelegramPolicyDirty.value)
watch(isFormDirty, (val) => emit('update:dirty', val), { immediate: true })

watch(bot, (value) => {
  if (!isTelegram.value || !value) return
  const metadata = isRecord(value.metadata) ? value.metadata : {}
  const rate = normalizeTelegramPassiveRate(metadata[TELEGRAM_PASSIVE_RATE_KEY])
  const keywords = normalizeTelegramKeywords(metadata[TELEGRAM_FORCE_REPLY_KEYWORDS_KEY])
  const sendFallbackEnabled = metadata[TELEGRAM_SEND_FALLBACK_ENABLED_KEY] === true
  const metadataMode = normalizeTelegramMessageMetadataMode(metadata[TELEGRAM_MESSAGE_METADATA_MODE_KEY])
  const toolCallsEnabled = metadata[TELEGRAM_TOOL_CALLS_ENABLED_KEY] !== false
  const skillsEnabled = metadata[TELEGRAM_SKILLS_ENABLED_KEY] !== false
  const hasToolPolicy = Array.isArray(metadata[TELEGRAM_ENABLED_TOOLS_KEY])
  const enabledTools = hasToolPolicy
    ? normalizeTelegramToolNames(metadata[TELEGRAM_ENABLED_TOOLS_KEY])
    : telegramTools.value.map(tool => tool.name)
  telegramPassiveRate.value = rate
  telegramForceReplyKeywords.value = keywords
  telegramSendFallbackEnabled.value = sendFallbackEnabled
  telegramMessageMetadataMode.value = metadataMode
  telegramToolCallsEnabled.value = toolCallsEnabled
  telegramSkillsEnabled.value = skillsEnabled
  telegramToolPolicyConfigured.value = hasToolPolicy
  telegramEnabledTools.value = enabledTools
  savedTelegramPassiveRate.value = rate
  savedTelegramForceReplyKeywords.value = [...keywords]
  savedTelegramSendFallbackEnabled.value = sendFallbackEnabled
  savedTelegramMessageMetadataMode.value = metadataMode
  savedTelegramToolCallsEnabled.value = toolCallsEnabled
  savedTelegramSkillsEnabled.value = skillsEnabled
  savedTelegramToolPolicyConfigured.value = hasToolPolicy
  savedTelegramEnabledTools.value = [...enabledTools]
  telegramPolicyLoaded.value = true
}, { immediate: true })

watch(telegramTools, (tools) => {
  if (!telegramPolicyLoaded.value || telegramToolPolicyConfigured.value) return
  const names = tools.map(tool => tool.name)
  telegramEnabledTools.value = names
  savedTelegramEnabledTools.value = [...names]
}, { immediate: true })

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}

function normalizeTelegramPassiveRate(value: unknown) {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 && value <= 1
    ? value
    : DEFAULT_TELEGRAM_PASSIVE_RATE
}

function normalizeTelegramKeywords(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  const seen = new Set<string>()
  const keywords: string[] = []
  for (const item of value) {
    if (typeof item !== 'string') continue
    const keyword = item.trim()
    const identity = keyword.toLocaleLowerCase()
    if (!keyword || seen.has(identity)) continue
    seen.add(identity)
    keywords.push(keyword)
  }
  return keywords
}

function normalizeTelegramToolNames(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  const seen = new Set<string>()
  const names: string[] = []
  for (const item of value) {
    const name = typeof item === 'string' ? item.trim() : ''
    if (!name || seen.has(name)) continue
    seen.add(name)
    names.push(name)
  }
  return names
}

function normalizeTelegramMessageMetadataMode(value: unknown): 'compact' | 'full' {
  return value === 'full' ? 'full' : DEFAULT_TELEGRAM_MESSAGE_METADATA_MODE
}

async function saveTelegramPolicy() {
  if (!isTelegramPolicyDirty.value) return
  if (!bot.value || !telegramPolicyLoaded.value) {
    throw new Error('Telegram discuss policy is not loaded')
  }
  const metadata = isRecord(bot.value.metadata) ? { ...bot.value.metadata } : {}
  metadata[TELEGRAM_PASSIVE_RATE_KEY] = telegramPassiveRate.value
  metadata[TELEGRAM_FORCE_REPLY_KEYWORDS_KEY] = [...telegramForceReplyKeywords.value]
  metadata[TELEGRAM_SEND_FALLBACK_ENABLED_KEY] = telegramSendFallbackEnabled.value
  metadata[TELEGRAM_MESSAGE_METADATA_MODE_KEY] = telegramMessageMetadataMode.value
  metadata[TELEGRAM_TOOL_CALLS_ENABLED_KEY] = telegramToolCallsEnabled.value
  if (telegramToolPolicyConfigured.value) {
    metadata[TELEGRAM_ENABLED_TOOLS_KEY] = [...telegramEnabledTools.value]
  } else {
    delete metadata[TELEGRAM_ENABLED_TOOLS_KEY]
  }
  metadata[TELEGRAM_SKILLS_ENABLED_KEY] = telegramSkillsEnabled.value
  await putBotsById({ path: { id: botIdRef.value }, body: { metadata }, throwOnError: true })
  savedTelegramPassiveRate.value = telegramPassiveRate.value
  savedTelegramForceReplyKeywords.value = [...telegramForceReplyKeywords.value]
  savedTelegramSendFallbackEnabled.value = telegramSendFallbackEnabled.value
  savedTelegramMessageMetadataMode.value = telegramMessageMetadataMode.value
  savedTelegramToolCallsEnabled.value = telegramToolCallsEnabled.value
  savedTelegramSkillsEnabled.value = telegramSkillsEnabled.value
  savedTelegramToolPolicyConfigured.value = telegramToolPolicyConfigured.value
  savedTelegramEnabledTools.value = [...telegramEnabledTools.value]
  void queryCache.invalidateQueries({ key: ['bot', botIdRef.value] })
  void queryCache.invalidateQueries({ key: ['bot'] })
}

function updateTelegramPassiveRate(value: number[] | undefined) {
  telegramPassiveRate.value = value?.[0] ?? telegramPassiveRate.value
}

function isTelegramToolEnabled(name: string): boolean {
  return telegramEnabledTools.value.includes(name)
}

function setTelegramToolEnabled(name: string, enabled: boolean) {
  const current = new Set(telegramEnabledTools.value)
  if (enabled) current.add(name)
  else current.delete(name)
  const catalogOrder = telegramTools.value.map(tool => tool.name)
  const known = catalogOrder.filter(toolName => current.has(toolName))
  const unknown = telegramEnabledTools.value.filter(toolName => !catalogOrder.includes(toolName) && current.has(toolName))
  telegramEnabledTools.value = [...known, ...unknown]
  telegramToolPolicyConfigured.value = true
}

function setAllTelegramTools(enabled: boolean) {
  telegramEnabledTools.value = enabled ? telegramTools.value.map(tool => tool.name) : []
  telegramToolPolicyConfigured.value = true
}

function validateRequired(): boolean {
  for (const key of requiredFieldsKeys.value) {
    const val = form.credentials[key]
    if (!val || (typeof val === 'string' && val.trim() === '')) {
      toast.error(t('bots.channels.requiredField', { field: orderedFields.value[key]?.title || key }))
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
    await saveTelegramPolicy()
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
  if (webhookCallbackUrl.value && typeof navigator !== 'undefined' && navigator.clipboard) {
    await navigator.clipboard.writeText(webhookCallbackUrl.value)
    toast.success(t('common.copied'))
  } else {
    toast.error(t('bots.channels.copyFailed'))
  }
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
