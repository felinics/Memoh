<template>
  <InlineLoadingRow v-if="initialLoading">
    {{ t('common.loading') }}
  </InlineLoadingRow>

  <div
    v-else-if="loadFailed"
    class="flex items-center justify-between gap-3"
  >
    <p class="text-sm text-muted-foreground">
      {{ t('common.loadFailed') }}
    </p>
    <Button
      variant="outline"
      size="sm"
      @click="retry"
    >
      {{ t('runtimes.retry') }}
    </Button>
  </div>

  <template v-else>
    <SettingsSection v-if="rows.length || subject === 'bot'">
      <!-- Bot direction always lists the native workspace first: it is part
           of every bot and can never be revoked, so it gets a caption instead
           of a switch. -->
      <SettingsRow
        v-if="subject === 'bot'"
        :label="t('bots.remoteRuntime.nativeWorkspace')"
        :description="t('computerAccess.nativeAlwaysOn')"
      >
        <template #leading>
          <Cloud class="size-4 text-muted-foreground" />
        </template>
        <Switch
          :model-value="true"
          disabled
          :aria-label="t('bots.remoteRuntime.nativeWorkspace')"
        />
      </SettingsRow>

      <SettingsRow
        v-for="row in rows"
        :key="row.key"
        :label="row.name"
      >
        <template #leading>
          <Avatar
            v-if="row.kind === 'bot'"
            class="size-6"
          >
            <AvatarImage
              v-if="row.avatarUrl"
              :src="row.avatarUrl"
              :alt="row.name"
            />
            <AvatarFallback class="text-caption">
              {{ avatarInitials(row.name) }}
            </AvatarFallback>
          </Avatar>
          <Laptop
            v-else
            class="size-4 text-muted-foreground"
          />
        </template>
        <div class="flex items-center gap-2">
          <span
            v-if="row.kind === 'runtime'"
            class="flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground"
          >
            <span
              class="size-1.5 rounded-full"
              :class="row.online ? 'bg-success' : 'bg-accent-gray-border'"
            />
            {{ row.online ? t('runtimes.status.online') : t('runtimes.status.offline') }}
          </span>
          <Switch
            :model-value="overrides.get(row.key) ?? row.enabled"
            :disabled="pending.has(row.key)"
            :aria-label="row.name"
            @update:model-value="toggle(row, $event)"
          />
        </div>
      </SettingsRow>

      <!-- Bot direction with zero account computers: the connect CTA lives in
           the same frame, one row under the native workspace. -->
      <SettingsRow
        v-if="subject === 'bot' && rows.length === 0"
        :label="t('computerAccess.emptyComputers')"
      >
        <Button
          variant="outline"
          size="sm"
          @click="goToRuntimes"
        >
          {{ t('computerAccess.connectCta') }}
        </Button>
      </SettingsRow>
    </SettingsSection>

    <div
      v-else
      class="py-6 text-center"
    >
      <p class="text-sm text-muted-foreground">
        {{ t('computerAccess.emptyBots') }}
      </p>
    </div>
  </template>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useQuery } from '@pinia/colada'
import type { BotsBot } from '@memohai/sdk'
import { getBotsQuery } from '@memohai/sdk/colada'
import {
  Avatar,
  AvatarFallback,
  AvatarImage,
  Button,
  InlineLoadingRow,
  SettingsRow,
  SettingsSection,
  Switch,
  toast,
} from '@felinic/ui'
import { Cloud, Laptop } from 'lucide-vue-next'
import { avatarInitials } from '@/composables/useAvatarInitials'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { useAccountRuntimes, useComputerAccessActions, useComputerAccessGrants } from './use-computer-access'

// The framed bot↔computer ACL list, shared by the standalone access dialog
// and the connect stepper's permissions step. Exactly one subject prop is set:
// - runtime: rows are the account's bots.
// - bot: rows are the account's runtimes (plus the always-on native workspace).
const props = defineProps<{
  runtime?: { id: string, name: string } | null
  bot?: { id: string, name: string } | null
}>()

type AccessRow = {
  key: string
  botId: string
  runtimeId: string
  name: string
  enabled: boolean
  targetId?: string
} & (
  | { kind: 'bot', avatarUrl?: string }
  | { kind: 'runtime', online: boolean }
)

const { t } = useI18n()
const router = useRouter()

const subject = computed<'runtime' | 'bot'>(() => (props.runtime ? 'runtime' : 'bot'))

const { grants, isLoading: grantsLoading, error: grantsError, refetch: refetchGrants } = useComputerAccessGrants()
const { data: botsData, isLoading: botsLoading, error: botsError, refetch: refetchBots } = useQuery(getBotsQuery())
const { runtimes, isLoading: runtimesLoading, error: runtimesError, refetch: refetchRuntimes } = useAccountRuntimes()
const { grantAccess, revokeAccess } = useComputerAccessActions()

const initialLoading = computed(() => (
  (grantsLoading.value && !grants.value.length)
  || (subject.value === 'runtime' && botsLoading.value && !botsData.value)
  || (subject.value === 'bot' && runtimesLoading.value && !runtimes.value)
))
const loadFailed = computed(() => (
  (!!grantsError.value && !grants.value.length)
  || (subject.value === 'runtime' && !!botsError.value && !botsData.value)
  || (subject.value === 'bot' && !!runtimesError.value && !runtimes.value)
))

const rows = computed<AccessRow[]>(() => {
  if (subject.value === 'runtime') {
    const runtimeId = props.runtime?.id ?? ''
    return (botsData.value?.items ?? []).flatMap((bot: BotsBot): AccessRow[] => {
      if (!bot.id) return []
      const grant = grants.value.find(g => g.bot_id === bot.id && g.runtime_id === runtimeId)
      return [{
        kind: 'bot',
        key: `${bot.id}:${runtimeId}`,
        botId: bot.id,
        runtimeId,
        name: bot.display_name || bot.name || bot.id,
        avatarUrl: bot.avatar_url ?? undefined,
        enabled: !!grant,
        targetId: grant?.target_id,
      }]
    })
  }
  const botId = props.bot?.id ?? ''
  return (runtimes.value ?? []).flatMap((runtime): AccessRow[] => {
    if (!runtime.id) return []
    const grant = grants.value.find(g => g.bot_id === botId && g.runtime_id === runtime.id)
    return [{
      kind: 'runtime',
      key: `${botId}:${runtime.id}`,
      botId,
      runtimeId: runtime.id,
      name: runtime.name || runtime.hostname || runtime.id,
      online: runtime.online ?? false,
      enabled: !!grant,
      targetId: grant?.target_id,
    }]
  })
})

// Optimistic overlay: the switch flips immediately and holds until the local
// grants refetch lands; an error clears the overlay and the row reverts.
const overrides = ref(new Map<string, boolean>())
const pending = ref(new Set<string>())

async function toggle(row: AccessRow, enabled: boolean): Promise<void> {
  if (pending.value.has(row.key)) return
  if (!enabled && !row.targetId) return
  pending.value = new Set(pending.value).add(row.key)
  overrides.value = new Map(overrides.value).set(row.key, enabled)
  try {
    if (enabled) {
      await grantAccess({ botId: row.botId, runtimeId: row.runtimeId })
    } else {
      await revokeAccess({ botId: row.botId, targetId: row.targetId! })
    }
    await refetchGrants()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('computerAccess.updateFailed')))
  } finally {
    const nextPending = new Set(pending.value)
    nextPending.delete(row.key)
    pending.value = nextPending
    const nextOverrides = new Map(overrides.value)
    nextOverrides.delete(row.key)
    overrides.value = nextOverrides
  }
}

function retry(): void {
  void refetchGrants()
  if (subject.value === 'runtime') void refetchBots()
  else void refetchRuntimes()
}

function goToRuntimes(): void {
  void router.push({ name: 'runtimes' })
}
</script>
