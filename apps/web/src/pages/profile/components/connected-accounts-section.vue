<template>
  <SettingsSection :title="$t('settings.connectedAccounts.title')">
    <!-- No linkable IM bot: linking needs a bot that can receive /link, so the
         only useful action here is going to set one up. -->
    <SettingsRow
      v-if="!checkingImBot && !hasLinkableImBot"
      :label="$t('settings.connectedAccounts.noImBotTitle')"
      :description="$t('settings.connectedAccounts.noImBotHint')"
    >
      <Button
        variant="outline"
        size="sm"
        class="shrink-0"
        @click="goToBots"
      >
        {{ $t('settings.connectedAccounts.goToBots') }}
        <ArrowRight class="ml-1.5 size-3.5" />
      </Button>
    </SettingsRow>

    <!-- Loading bound identities. ui-allow-shape: borrows the SettingsRow height
         only to hold the row's vertical space steady while bindings load, so the
         list doesn't jump (CLS) when they arrive — it's a skeleton, not a data row. -->
    <div
      v-else-if="isLoading"
      class="mx-4 flex min-h-[3.75rem] items-center justify-center py-3"
    >
      <Spinner class="size-5 text-muted-foreground/50" />
    </div>

    <template v-else>
      <!-- Bound identities -->
      <SettingsRow
        v-for="binding in bindings"
        :key="binding.id"
      >
        <template #leading>
          <Avatar class="size-8">
            <AvatarImage
              :src="binding.channel_identity_avatar_url || ''"
              :alt="bindingLabel(binding)"
            />
            <AvatarFallback class="text-xs">
              {{ bindingLabel(binding).slice(0, 2).toUpperCase() }}
            </AvatarFallback>
          </Avatar>
        </template>
        <template #content>
          <div class="truncate text-sm font-medium text-foreground">
            {{ bindingLabel(binding) }}
          </div>
          <div
            v-if="binding.channel_type"
            class="mt-0.5 flex items-center gap-1 truncate text-xs text-muted-foreground"
          >
            <ChannelIcon
              :channel="binding.channel_type"
              size="1em"
            />
            <span>{{ channelTypeDisplayName(t, binding.channel_type) }}</span>
          </div>
        </template>
        <ConfirmPopover
          :message="$t('settings.connectedAccounts.disconnectConfirm')"
          :cancel-text="$t('common.cancel')"
          :confirm-text="$t('settings.connectedAccounts.disconnect')"
          @confirm="() => onDisconnect(binding)"
        >
          <template #trigger>
            <Button
              variant="ghost"
              size="icon-sm"
              class="shrink-0 text-muted-foreground hover:text-destructive"
              :disabled="isRowBusy(binding)"
            >
              <Trash2 class="size-4" />
            </Button>
          </template>
        </ConfirmPopover>
      </SettingsRow>

      <!-- Link action: empty-state title when none yet, otherwise "link another" -->
      <SettingsRow
        :label="bindings.length === 0 ? $t('settings.connectedAccounts.empty') : $t('settings.connectedAccounts.linkAnother')"
        :description="$t('settings.connectedAccounts.subtitle')"
      >
        <Button
          variant="outline"
          size="sm"
          class="shrink-0"
          :loading="issuing"
          @click="onIssue"
        >
          <Plus class="mr-1.5 size-3.5" />
          {{ $t('settings.connectedAccounts.connect') }}
        </Button>
      </SettingsRow>

      <!-- Active link code with live countdown -->
      <div
        v-if="activeCode"
        class="mx-4 space-y-2.5 border-b border-border py-4 last:border-b-0"
      >
        <div class="flex items-center justify-between gap-2">
          <p class="text-xs text-muted-foreground">
            {{ $t('settings.connectedAccounts.codeInstruction') }}
          </p>
          <span
            class="shrink-0 text-xs tabular-nums"
            :class="expired ? 'text-destructive' : 'text-muted-foreground'"
          >
            {{ expired ? $t('settings.connectedAccounts.expired') : $t('settings.connectedAccounts.expiresIn', { time: remainingLabel }) }}
          </span>
        </div>
        <div class="flex items-center gap-2">
          <Input
            :model-value="`/link ${activeCode}`"
            readonly
            tabindex="-1"
            size="sm"
            class="flex-1 cursor-default select-none pointer-events-none font-mono [&:read-only:not(:disabled)]:bg-transparent [&:read-only:not(:disabled)]:text-foreground [&:read-only:not(:disabled)]:cursor-default"
            :class="expired ? 'line-through opacity-60' : ''"
          />
          <Button
            v-if="!expired"
            variant="outline"
            size="sm"
            class="shrink-0"
            @click="copyCode"
          >
            <Check
              v-if="copied"
              class="mr-1.5 size-3.5"
            />
            <Copy
              v-else
              class="mr-1.5 size-3.5"
            />
            {{ copied ? $t('settings.connectedAccounts.copied') : $t('settings.connectedAccounts.copy') }}
          </Button>
          <Button
            v-else
            variant="outline"
            size="sm"
            class="shrink-0"
            :loading="issuing"
            @click="onIssue"
          >
            <RefreshCw class="mr-1.5 size-3.5" />
            {{ $t('settings.connectedAccounts.connect') }}
          </Button>
        </div>
      </div>
    </template>
  </SettingsSection>
</template>

<script setup lang="ts">
import { ref, computed, watch, onActivated, onBeforeUnmount } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useQuery, useQueryCache } from '@pinia/colada'
import { Plus, Trash2, Copy, Check, RefreshCw, ArrowRight } from 'lucide-vue-next'
import { ConfirmPopover, SettingsRow, SettingsSection, toast, useClipboard } from '@felinic/ui'
import {
  Button,
  Spinner,
  Avatar,
  AvatarImage,
  AvatarFallback,
  Input,
} from '@felinic/ui'
import ChannelIcon from '@/components/channel-icon/index.vue'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { channelTypeDisplayName } from '@/utils/channel-type-label'
import {
  getBots,
  getChannels,
  getBotsByIdChannelByPlatform,
  getUsersMeChannelIdentities,
  postUsersMeChannelLinks,
  deleteUsersMeChannelIdentitiesByChannelIdentityId,
} from '@memohai/sdk'
import type { ChannelaccessBinding } from '@memohai/sdk'

const { t } = useI18n()
const router = useRouter()
const queryCache = useQueryCache()

const issuing = ref(false)
const activeCode = ref('')
const expiresAtMs = ref(0)
const nowMs = ref(Date.now())
const copied = ref(false)
const busyIds = ref<Set<string>>(new Set())

// Account count captured when a code is issued. Once a new binding appears past
// this baseline, the code has been consumed — so the code block is cleared.
const bindingsCountAtIssue = ref(0)

function clearActiveCode() {
  activeCode.value = ''
  expiresAtMs.value = 0
  copied.value = false
  stopTicker()
}

let ticker: ReturnType<typeof setInterval> | undefined
let pollTick = 0

function stopTicker() {
  if (ticker) {
    clearInterval(ticker)
    ticker = undefined
  }
}

onBeforeUnmount(stopTicker)

const expired = computed(() => !!activeCode.value && expiresAtMs.value > 0 && nowMs.value >= expiresAtMs.value)
const remainingLabel = computed(() => {
  const ms = Math.max(0, expiresAtMs.value - nowMs.value)
  const totalSec = Math.ceil(ms / 1000)
  const m = Math.floor(totalSec / 60)
  const s = totalSec % 60
  return `${m}:${String(s).padStart(2, '0')}`
})

const { data: bindingsData, isLoading, refetch: refetchBindings } = useQuery({
  key: () => ['my-channel-identities'],
  query: async () => {
    const { data } = await getUsersMeChannelIdentities({ throwOnError: true })
    return data
  },
})

const bindings = computed<ChannelaccessBinding[]>(() => bindingsData.value?.items ?? [])
const { copyText } = useClipboard()

// A live code is being polled in the background; when a new account shows up the
// code has just been used, so retire it instead of leaving a stale countdown.
watch(() => bindings.value.length, (count) => {
  if (activeCode.value && count > bindingsCountAtIssue.value) {
    clearActiveCode()
  }
})

// Whether the user has any bot connected to an IM channel that could receive /link.
const { data: hasImBotData, isLoading: checkingImBot, refetch: refetchHasImBot } = useQuery({
  key: () => ['user-has-im-bot'],
  query: async (): Promise<boolean> => {
    const [botsRes, channelsRes] = await Promise.all([
      getBots({ throwOnError: true }),
      getChannels({ throwOnError: true }),
    ])
    const imPlatforms = (channelsRes.data ?? [])
      .filter(m => !m.configless && m.type)
      .map(m => m.type as string)
    const botList = botsRes.data?.items ?? []
    if (imPlatforms.length === 0 || botList.length === 0) return false
    for (const bot of botList) {
      const id = bot.id?.trim()
      if (!id) continue
      const hits = await Promise.all(imPlatforms.map(async (platform) => {
        try {
          const { data } = await getBotsByIdChannelByPlatform({ path: { id, platform }, throwOnError: true })
          return !data?.disabled
        }
        catch {
          return false
        }
      }))
      if (hits.some(Boolean)) return true
    }
    return false
  },
})

const hasLinkableImBot = computed(() => hasImBotData.value === true)

// Settings pages stay alive under <KeepAlive>, so the IM-bot check and bindings
// keep a stale value after the user connects a channel elsewhere and returns.
// Re-fetch both whenever the page is re-activated to reflect the latest state.
onActivated(() => {
  void refetchHasImBot()
  void refetchBindings()
})

function goToBots() {
  void router.push({ name: 'bots' })
}

function bindingLabel(binding: ChannelaccessBinding): string {
  return binding.channel_identity_display_name
    || binding.channel_subject_id
    || binding.channel_identity_id
    || ''
}

function isRowBusy(binding: ChannelaccessBinding): boolean {
  return !!binding.channel_identity_id && busyIds.value.has(binding.channel_identity_id)
}

function invalidate() {
  return queryCache.invalidateQueries({ key: ['my-channel-identities'] })
}

function startTicker() {
  stopTicker()
  nowMs.value = Date.now()
  pollTick = 0
  ticker = setInterval(() => {
    nowMs.value = Date.now()
    // While the code is live, the user is likely completing /link in IM, so poll
    // the bindings list (every ~3s) to surface the new account without a manual
    // refresh. Stop ticking once the code expires.
    if (expiresAtMs.value > 0 && nowMs.value >= expiresAtMs.value) {
      stopTicker()
      return
    }
    pollTick += 1
    if (pollTick % 3 === 0) {
      void refetchBindings()
    }
  }, 1000)
}

async function onIssue() {
  if (issuing.value) return
  issuing.value = true
  try {
    const { data } = await postUsersMeChannelLinks({ body: {}, throwOnError: true })
    bindingsCountAtIssue.value = bindings.value.length
    activeCode.value = data?.token ?? ''
    expiresAtMs.value = data?.expires_at ? new Date(data.expires_at).getTime() : 0
    copied.value = false
    startTicker()
  }
  catch (error) {
    toast.error(resolveApiErrorMessage(error, t('settings.connectedAccounts.issueFailed')))
  }
  finally {
    issuing.value = false
  }
}

async function copyCode() {
  if (!activeCode.value || expired.value) return
  const ok = await copyText(`/link ${activeCode.value}`)
  if (!ok) {
    toast.error(t('common.copyFailed'))
    return
  }
  copied.value = true
  setTimeout(() => (copied.value = false), 2000)
}

async function onDisconnect(binding: ChannelaccessBinding) {
  const identityId = binding.channel_identity_id
  if (!identityId) return
  busyIds.value.add(identityId)
  try {
    await deleteUsersMeChannelIdentitiesByChannelIdentityId({
      path: { channel_identity_id: identityId },
      throwOnError: true,
    })
    await invalidate()
    toast.success(t('settings.connectedAccounts.disconnected'))
  }
  catch (error) {
    toast.error(resolveApiErrorMessage(error, t('settings.connectedAccounts.disconnectFailed')))
  }
  finally {
    busyIds.value.delete(identityId)
  }
}
</script>
