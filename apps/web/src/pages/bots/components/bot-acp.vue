<template>
  <SwapTransition :direction="direction">
    <!-- List: one agent per row. The card navigates into its setup; the Switch
         is the only enable affordance and stays on this list, never on the
         setup page (so enabling never unfurls a long inline form). -->
    <PageShell
      v-if="view === 'list'"
      variant="tab"
      :title="$t('bots.tabs.acp')"
    >
      <div
        v-if="profilesLoading && profiles.length === 0"
        class="space-y-3"
      >
        <Skeleton
          v-for="n in 2"
          :key="n"
          class="h-[4.5rem] w-full rounded-[var(--radius-menu-shell)]"
        />
      </div>

      <Empty
        v-else-if="profiles.length === 0"
        class="rounded-[var(--radius-menu-shell)] border border-dashed border-border py-16"
      >
        <EmptyTitle>{{ $t('bots.settings.acpEmptyTitle') }}</EmptyTitle>
        <EmptyDescription>{{ $t('bots.settings.acpEmptyDescription') }}</EmptyDescription>
      </Empty>

      <div
        v-else
        class="space-y-3"
      >
        <div
          v-for="profile in profiles"
          :key="profile.id"
          class="relative flex items-center gap-3 rounded-[var(--radius-menu-shell)] border border-border bg-card p-3.5 transition-colors hover:bg-accent/30 dark:hover:bg-accent"
        >
          <!-- Stretched navigate target: fills the card so the whole row opens
               setup, while the Switch above keeps its own click. -->
          <button
            type="button"
            class="absolute inset-0 rounded-[var(--radius-menu-shell)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            :aria-label="profile.display_name || profile.id"
            @click="openAgent(profile)"
          />

          <span class="pointer-events-none relative flex size-10 shrink-0 items-center justify-center rounded-full bg-muted">
            <component
              :is="acpAgentIcon(profile.id, true)"
              class="size-5"
            />
            <!-- Green dot: on + ready (healthy state — small, says nothing more). -->
            <StatusDot
              v-if="agentRowState(profile) === 'on_ready'"
              status="success"
              class="absolute -bottom-0.5 -right-0.5 size-2.5! ring-2 ring-card"
            />
          </span>

          <span class="pointer-events-none relative min-w-0 flex-1">
            <span class="block truncate text-sm font-medium text-foreground">
              {{ profile.display_name || profile.id }}
            </span>
            <span
              v-if="profile.description"
              class="mt-0.5 block truncate text-xs text-muted-foreground"
            >
              {{ profile.description }}
            </span>
          </span>

          <div class="relative flex shrink-0 items-center gap-3">
            <!-- Row status: surfaced as a Badge (aligns to a region, not a loose dot).
                 Needs-config is actionable so it earns its place; "Disabled" distinguishes
                 a previously-configured agent from one never touched. -->
            <Badge
              v-if="agentRowState(profile) === 'on_needs_config'"
              variant="outline"
              size="sm"
              class="border-warning/30 text-warning"
            >
              {{ $t('bots.settings.acpStatusNeedsConfig') }}
            </Badge>
            <Badge
              v-else-if="agentRowState(profile) === 'off_configured'"
              variant="outline"
              size="sm"
            >
              {{ $t('bots.settings.acpStatusOff') }}
            </Badge>
            <ChevronRight class="size-4 text-muted-foreground/60" />
            <Switch
              :model-value="agentForm(profile).enabled"
              :aria-label="profile.display_name || profile.id"
              @update:model-value="(val) => setAgentEnabled(profile, !!val)"
            />
          </div>
        </div>
      </div>
    </PageShell>

    <!-- Setup: configuration for the selected agent only. The top padding and
         back-button margin mirror the list view's PageShell (pt-6 tab variant +
         mb-6 title-to-body gap), so the back arrow lands at the same height as
         the list page's title and the gap to the first card is the same. -->
    <section
      v-else
      class="mx-auto max-w-3xl pt-6 pb-8"
    >
      <Button
        variant="ghost"
        class="mb-6 text-foreground/85"
        @click="closeDetail()"
      >
        <ChevronLeft class="size-4" />
        {{ $t('bots.tabs.acp') }}
      </Button>

      <SettingsAcpDetail
        v-if="selectedProfile"
        :key="`${botId}:${selectedProfile.id}`"
        :bot-id="botId"
        :profile="selectedProfile"
        :form="form"
        :pending-self-confirm="selectedPendingHermesSelfConfirm"
        @commit="handleDetailCommit"
      />
    </section>
  </SwapTransition>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { Badge, Button, Empty, EmptyDescription, EmptyTitle, PageShell, Skeleton, StatusDot, SwapTransition, Switch, toast } from '@felinic/ui'
import { ChevronLeft, ChevronRight } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { useMutation, useQuery, useQueryCache } from '@pinia/colada'
import { getAcpProfiles, getBotsById, putBotsById } from '@memohai/sdk'
import type { AcpprofilePublicProfile, BotsUpdateBotRequest } from '@memohai/sdk'
import type { Ref } from 'vue'
import SettingsAcpDetail from './settings-acp-detail.vue'
import { useViewSwap } from '@/composables/useViewSwap'
import { resolveApiErrorMessage } from '@/utils/api-error'
import {
  acpAgentIcon,
  emptyACPAgentForm,
  ensureACPAgentForm,
  findMissingRequiredManagedField,
  normalizeACPAgentID,
  normalizeACPForm,
  readACPConfig,
  withACPMetadata,
  type ACPAgentForm,
  type ACPForm,
} from '@/utils/acp'
import { useChatStore } from '@/store/chat-list'

const props = defineProps<{
  botId: string
}>()

const { t } = useI18n()
const queryCache = useQueryCache()
const chatStore = useChatStore()
const botIdRef = computed(() => props.botId) as Ref<string>

const form = reactive<ACPForm>({
  agents: {},
})
const lastPersistedSnapshot = ref('')
const persistRunning = ref(false)
const persistQueued = ref(false)
const pendingHermesSelfConfirm = ref<Set<string>>(new Set())

const { view, direction, openDetail, backToList } = useViewSwap()
const selectedId = ref('')

const { data: profileData, isLoading: profilesLoading } = useQuery({
  key: () => ['acp-profiles'],
  query: async () => {
    const { data } = await getAcpProfiles({ throwOnError: true })
    return data
  },
})

const profiles = computed<AcpprofilePublicProfile[]>(() => profileData.value?.items ?? [])

const selectedProfile = computed(() =>
  profiles.value.find(p => normalizeACPAgentID(p.id) === selectedId.value) ?? null,
)
const selectedPendingHermesSelfConfirm = computed(() =>
  selectedId.value ? pendingHermesSelfConfirm.value.has(selectedId.value) : false,
)

const { data: bot } = useQuery({
  key: () => ['bot', botIdRef.value],
  query: async () => {
    const { data } = await getBotsById({ path: { id: botIdRef.value }, throwOnError: true })
    return data
  },
  enabled: () => !!botIdRef.value,
})

const { mutateAsync: updateBot } = useMutation({
  mutation: async (body: BotsUpdateBotRequest) => {
    const { data } = await putBotsById({
      path: { id: botIdRef.value },
      body,
      throwOnError: true,
    })
    return data
  },
  onSettled: () => {
    queryCache.invalidateQueries({ key: ['bot', botIdRef.value] })
    queryCache.invalidateQueries({ key: ['bots'] })
    void chatStore.refreshBots().catch(() => {})
  },
})

watch([bot, profiles], ([value, list]) => {
  applyMetadataToForm(value?.metadata as Record<string, unknown> | undefined, list)
}, { immediate: true })

// If the open agent vanishes after a profile refetch, fall back to the list.
watch(profiles, (list) => {
  if (view.value === 'detail' && selectedId.value && !list.some(p => normalizeACPAgentID(p.id) === selectedId.value)) {
    closeDetail()
  }
})

function agentForm(profile: AcpprofilePublicProfile): ACPAgentForm {
  return ensureACPAgentForm(form, profile)
}

function openAgent(profile: AcpprofilePublicProfile) {
  selectedId.value = normalizeACPAgentID(profile.id)
  openDetail()
}

function setAgentEnabled(profile: AcpprofilePublicProfile, enabled: boolean) {
  const id = normalizeACPAgentID(profile.id)
  const agent = agentForm(profile)
  agent.enabled = enabled
  if (enabled && shouldOpenAgentOnEnable(profile)) {
    openAgent(profile)
    if (isHermesSelfMode(profile)) {
      if (id) pendingHermesSelfConfirm.value.add(id)
      return
    }
    if (agentNeedsConfig(profile)) return
  }
  if (id) pendingHermesSelfConfirm.value.delete(id)
  void persistACPForm()
}

function shouldOpenAgentOnEnable(profile: AcpprofilePublicProfile): boolean {
  return agentNeedsConfig(profile) || isHermesSelfMode(profile)
}

function agentNeedsConfig(profile: AcpprofilePublicProfile): boolean {
  const agent = agentForm(profile)
  if (agent.setup_mode === 'self') return false
  return findMissingRequiredManagedField(profile, agent.managed, agent.setup_mode) !== null
}

function isHermesSelfMode(profile: AcpprofilePublicProfile): boolean {
  return normalizeACPAgentID(profile.id) === 'hermes' && agentForm(profile).setup_mode === 'self'
}

// Drives the row's status Badge / dot. Four honest states:
//   off_empty      — never touched (no credentials, disabled): show nothing.
//   off_configured — disabled but has saved credentials (distinct from "never used").
//   on_needs_config — enabled but missing required credentials: actionable hint.
//   on_ready       — enabled and ready: a small green dot, nothing more.
function agentRowState(profile: AcpprofilePublicProfile): 'off_empty' | 'off_configured' | 'on_needs_config' | 'on_ready' {
  const agent = agentForm(profile)
  const hasCredentials = Object.values(agent.managed).some(v => String(v ?? '').trim() !== '')
  if (!agent.enabled) return hasCredentials ? 'off_configured' : 'off_empty'
  return agentNeedsConfig(profile) ? 'on_needs_config' : 'on_ready'
}

async function persistACPForm() {
  if (!bot.value) return
  if (persistRunning.value) {
    persistQueued.value = true
    return
  }
  const normalized = normalizedFormForPersist()
  const snapshot = JSON.stringify(normalized)
  if (snapshot === lastPersistedSnapshot.value) return
  persistRunning.value = true
  try {
    await updateBot({
      metadata: withACPMetadata(
        bot.value?.metadata as Record<string, unknown> | undefined,
        normalized,
        profiles.value,
      ),
    })
    lastPersistedSnapshot.value = snapshot
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
    if (!persistQueued.value) {
      applyMetadataToForm(bot.value?.metadata as Record<string, unknown> | undefined, profiles.value, true)
    }
  } finally {
    persistRunning.value = false
    if (persistQueued.value) {
      persistQueued.value = false
      void persistACPForm()
    }
  }
}

interface ACPCommitOptions {
  confirmSelf?: boolean
}

function handleDetailCommit(options?: ACPCommitOptions) {
  const id = selectedId.value
  if (id && (options?.confirmSelf || !selectedProfile.value || !isHermesSelfMode(selectedProfile.value))) {
    pendingHermesSelfConfirm.value.delete(id)
  }
  void persistACPForm()
}

function closeDetail() {
  discardPendingHermesSelfConfirm()
  backToList()
}

function normalizedFormForPersist(): ACPForm {
  const normalized = normalizeACPForm(form, profiles.value)
  const persisted = parsePersistedSnapshot()
  for (const id of pendingHermesSelfConfirm.value) {
    const existing = persisted?.agents?.[id]
    if (existing) {
      normalized.agents[id] = {
        enabled: existing.enabled,
        setup_mode: existing.setup_mode,
        managed: { ...existing.managed },
      }
      continue
    }
    if (normalized.agents[id]) {
      normalized.agents[id].enabled = false
    }
  }
  return normalized
}

function discardPendingHermesSelfConfirm() {
  const id = selectedId.value
  if (!id || !pendingHermesSelfConfirm.value.has(id)) return
  const profile = selectedProfile.value
  const persisted = parsePersistedSnapshot()?.agents?.[id]
  if (persisted) {
    form.agents[id] = {
      enabled: persisted.enabled,
      setup_mode: persisted.setup_mode,
      managed: { ...persisted.managed },
    }
  } else if (profile) {
    form.agents[id] = emptyACPAgentForm(profile)
  }
  pendingHermesSelfConfirm.value.delete(id)
}

function parsePersistedSnapshot(): ACPForm | null {
  if (!lastPersistedSnapshot.value) return null
  try {
    return JSON.parse(lastPersistedSnapshot.value) as ACPForm
  } catch {
    return null
  }
}

function applyMetadataToForm(metadata: Record<string, unknown> | undefined, list: AcpprofilePublicProfile[], force = false) {
  const next = readACPConfig(metadata, list)
  const nextSnapshot = JSON.stringify(next)
  const currentSnapshot = JSON.stringify(normalizeACPForm(form, list))
  if (!force && (persistRunning.value || persistQueued.value || currentSnapshot !== lastPersistedSnapshot.value) && nextSnapshot === lastPersistedSnapshot.value) {
    return
  }
  for (const key of Object.keys(form.agents)) {
    if (!next.agents[key]) delete form.agents[key]
  }
  for (const profile of list) {
    const id = normalizeACPAgentID(profile.id)
    if (!id) continue
    form.agents[id] = next.agents[id] ?? emptyACPAgentForm(profile)
  }
  lastPersistedSnapshot.value = nextSnapshot
}
</script>
