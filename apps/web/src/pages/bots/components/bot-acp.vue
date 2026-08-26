<template>
  <SwapTransition :direction="direction">
    <PageShell
      v-if="view === 'list'"
      variant="tab"
      :title="t('bots.tabs.acp')"
    >
      <template #actions>
        <Button @click="addOpen = true">
          <Plus />
          {{ t('bots.agent.add') }}
        </Button>
      </template>

      <div
        v-if="agentsLoading && agents.length === 0"
        class="space-y-3"
      >
        <Skeleton
          v-for="n in 2"
          :key="n"
          class="h-[4.5rem] w-full rounded-[var(--radius-menu-shell)]"
        />
      </div>

      <Empty
        v-else-if="agents.length === 0"
        class="rounded-[var(--radius-menu-shell)] border border-dashed border-border py-16"
      >
        <EmptyHeader>
          <EmptyTitle>{{ t('bots.agent.emptyTitle') }}</EmptyTitle>
          <EmptyDescription>{{ t('bots.agent.emptyDescription') }}</EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <Button
            variant="outline"
            @click="addOpen = true"
          >
            <Plus />
            {{ t('bots.agent.add') }}
          </Button>
        </EmptyContent>
      </Empty>

      <div
        v-else
        class="space-y-3"
      >
        <div
          v-for="agent in agents"
          :key="agent.id"
          class="relative flex items-center gap-3 rounded-[var(--radius-menu-shell)] border border-border bg-card p-3.5 transition-colors hover:bg-accent/30 dark:hover:bg-accent"
        >
          <button
            type="button"
            class="absolute inset-0 rounded-[var(--radius-menu-shell)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            :aria-label="botAgentName(agent)"
            @click="openAgent(agent)"
          />

          <span class="pointer-events-none relative flex size-10 shrink-0 items-center justify-center rounded-full bg-muted">
            <component
              :is="botAgentIcon(agent, true)"
              class="size-5"
            />
            <StatusDot
              v-if="agentRowState(agent) === 'on_ready'"
              status="success"
              class="absolute -bottom-0.5 -right-0.5 size-2.5! ring-2 ring-card"
            />
          </span>

          <span class="pointer-events-none relative min-w-0 flex-1">
            <span class="block truncate text-sm font-medium text-foreground">
              {{ botAgentName(agent) }}
            </span>
            <span class="mt-0.5 block truncate text-xs text-muted-foreground">
              {{ providerLabel(agent) }}
            </span>
          </span>

          <div class="relative flex shrink-0 items-center gap-3">
            <Badge
              v-if="agentRowState(agent) === 'on_needs_config'"
              variant="outline"
              size="sm"
              class="border-warning/30 text-warning"
            >
              {{ t('bots.agent.statusNeedsConfig') }}
            </Badge>
            <Badge
              v-else-if="agentRowState(agent) === 'off'"
              variant="outline"
              size="sm"
            >
              {{ t('bots.agent.statusOff') }}
            </Badge>

            <DropdownMenu>
              <DropdownMenuTrigger as-child>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  :aria-label="t('common.actions')"
                  @click.stop
                >
                  <MoreHorizontal />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem
                  variant="destructive"
                  @select="deleteTarget = agent"
                >
                  <Trash2 />
                  {{ t('common.delete') }}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>

            <ChevronRight class="size-4 text-muted-foreground/60" />
            <Switch
              :model-value="agent.enabled !== false"
              :disabled="busyAgentIDs.has(agent.id ?? '')"
              :aria-label="botAgentName(agent)"
              @update:model-value="(value) => setAgentEnabled(agent, !!value)"
            />
          </div>
        </div>
      </div>

      <AddBotAgentDialog
        v-model:open="addOpen"
        :bot-id="botId"
        :profiles="profiles"
        :agents="agents"
        :bot-metadata="botMetadata"
        @created="openAgent"
      />

      <ConfirmDeleteDialog
        :open="!!deleteTarget"
        :title="t('bots.agent.deleteTitle')"
        :description="t('bots.agent.deleteDescription', { name: botAgentName(deleteTarget) })"
        :cancel-label="t('common.cancel')"
        :confirm-label="t('common.delete')"
        :loading="deleting"
        @update:open="value => { if (!value) deleteTarget = null }"
        @confirm="confirmDelete"
      />
    </PageShell>

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
        {{ t('bots.tabs.acp') }}
      </Button>

      <SettingsSection
        v-if="selectedAgent"
        class="mb-8"
      >
        <SettingsRow
          :label="t('common.name')"
          :description="t('bots.agent.nameDescription')"
          stack="sm"
        >
          <Input
            v-model="selectedName"
            class="w-full sm:w-56"
            :aria-label="t('common.name')"
            @blur="saveSelectedName"
            @keydown.enter.prevent="saveSelectedName"
          />
        </SettingsRow>
      </SettingsSection>

      <SettingsAcpDetail
        v-if="selectedAgent && selectedProfile"
        :key="`${botId}:${selectedAgent.id}:${selectedProfile.id}`"
        :bot-id="botId"
        :profile="selectedProfile"
        :form="form"
        @commit="persistACPForm"
      />
    </section>
  </SwapTransition>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMutation, useQuery, useQueryCache } from '@pinia/colada'
import {
  Badge,
  Button,
  ConfirmDeleteDialog,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
  Input,
  PageShell,
  SettingsRow,
  SettingsSection,
  Skeleton,
  StatusDot,
  SwapTransition,
  Switch,
  toast,
} from '@felinic/ui'
import { ChevronLeft, ChevronRight, MoreHorizontal, Plus, Trash2 } from 'lucide-vue-next'
import {
  deleteBotsByBotIdAgentsById,
  getAcpProfiles,
  getBotsByBotIdAgents,
  getBotsById,
  patchBotsByBotIdAgentsById,
  putBotsById,
  type AcpprofilePublicProfile,
  type BotagentsBotAgent,
  type BotsUpdateBotRequest,
} from '@memohai/sdk'
import { getBotsQueryKey } from '@memohai/sdk/colada'
import type { Ref } from 'vue'
import SettingsAcpDetail from './settings-acp-detail.vue'
import AddBotAgentDialog from './add-bot-agent-dialog.vue'
import { useViewSwap } from '@/composables/useViewSwap'
import { resolveApiErrorMessage } from '@/utils/api-error'
import {
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
import { botAgentIcon, botAgentName, botAgentProvider } from '@/utils/bot-agent'
import { useChatStore } from '@/store/chat-list'

const props = defineProps<{ botId: string }>()
const { t } = useI18n()
const queryCache = useQueryCache()
const chatStore = useChatStore()
const botIdRef = computed(() => props.botId) as Ref<string>

const form = reactive<ACPForm>({ agents: {} })
const lastPersistedSnapshot = ref('')
const persistRunning = ref(false)
const persistQueued = ref(false)
const busyAgentIDs = reactive(new Set<string>())
const addOpen = ref(false)
const deleteTarget = ref<BotagentsBotAgent | null>(null)
const deleting = ref(false)

const { view, direction, openDetail, backToList } = useViewSwap()
const selectedID = ref('')
const selectedName = ref('')

const { data: profileData } = useQuery({
  key: () => ['acp-profiles'],
  query: async () => {
    const { data } = await getAcpProfiles({ throwOnError: true })
    return data
  },
})
const profiles = computed<AcpprofilePublicProfile[]>(() => profileData.value?.items ?? [])

const { data: agentData, isLoading: agentsLoading } = useQuery({
  key: () => ['bot-agents', botIdRef.value],
  query: async () => {
    const { data } = await getBotsByBotIdAgents({
      path: { bot_id: botIdRef.value },
      throwOnError: true,
    })
    return data
  },
  enabled: () => !!botIdRef.value,
})
const agents = computed<BotagentsBotAgent[]>(() => agentData.value?.items ?? [])

const { data: bot } = useQuery({
  key: () => ['bot', botIdRef.value],
  query: async () => {
    const { data } = await getBotsById({ path: { id: botIdRef.value }, throwOnError: true })
    return data
  },
  enabled: () => !!botIdRef.value,
})
const botMetadata = computed(() => bot.value?.metadata as Record<string, unknown> | undefined)

const selectedAgent = computed(() => agents.value.find(agent => agent.id === selectedID.value) ?? null)
const selectedProfile = computed(() => {
  const provider = botAgentProvider(selectedAgent.value)
  return profiles.value.find(profile => normalizeACPAgentID(profile.id) === provider) ?? null
})

const { mutateAsync: updateAgent } = useMutation({
  mutation: async ({ agent, body }: { agent: BotagentsBotAgent; body: { name?: string; enabled?: boolean } }) => {
    const { data } = await patchBotsByBotIdAgentsById({
      path: { bot_id: props.botId, id: agent.id ?? '' },
      body,
      throwOnError: true,
    })
    return data
  },
  onSettled: () => {
    void queryCache.invalidateQueries({ key: ['bot-agents', props.botId] })
    void queryCache.invalidateQueries({ key: ['bot-settings', props.botId] })
  },
})

const { mutateAsync: updateBot } = useMutation({
  mutation: async (body: BotsUpdateBotRequest) => {
    const { data } = await putBotsById({ path: { id: props.botId }, body, throwOnError: true })
    return data
  },
  onSettled: () => {
    void queryCache.invalidateQueries({ key: ['bot', props.botId] })
    void queryCache.invalidateQueries({ key: getBotsQueryKey() })
    void chatStore.refreshBots().catch(() => {})
  },
})

watch([bot, profiles], ([value, list]) => {
  applyMetadataToForm(value?.metadata as Record<string, unknown> | undefined, list)
}, { immediate: true })

watch(selectedAgent, (agent) => {
  selectedName.value = botAgentName(agent)
})

watch(agents, (list) => {
  if (view.value === 'detail' && selectedID.value && !list.some(agent => agent.id === selectedID.value)) closeDetail()
})

function profileFor(agent: BotagentsBotAgent): AcpprofilePublicProfile | null {
  const provider = botAgentProvider(agent)
  return profiles.value.find(profile => normalizeACPAgentID(profile.id) === provider) ?? null
}

function providerLabel(agent: BotagentsBotAgent): string {
  const profile = profileFor(agent)
  return profile?.display_name?.trim() || botAgentProvider(agent)
}

function agentForm(profile: AcpprofilePublicProfile): ACPAgentForm {
  return ensureACPAgentForm(form, profile)
}

function agentNeedsConfig(agent: BotagentsBotAgent): boolean {
  const profile = profileFor(agent)
  if (!profile) return true
  const config = agentForm(profile)
  if (config.setup_mode === 'self') return false
  return findMissingRequiredManagedField(profile, config.managed, config.setup_mode) !== null
}

function agentRowState(agent: BotagentsBotAgent): 'off' | 'on_needs_config' | 'on_ready' {
  if (agent.enabled === false) return 'off'
  return agentNeedsConfig(agent) ? 'on_needs_config' : 'on_ready'
}

function openAgent(agent: BotagentsBotAgent) {
  if (!agent.id) return
  selectedID.value = agent.id
  selectedName.value = botAgentName(agent)
  openDetail()
}

async function setAgentEnabled(agent: BotagentsBotAgent, enabled: boolean) {
  const id = agent.id ?? ''
  if (!id || busyAgentIDs.has(id)) return
  busyAgentIDs.add(id)
  try {
    await updateAgent({ agent, body: { enabled } })
    if (enabled && agentNeedsConfig(agent)) openAgent(agent)
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
  } finally {
    busyAgentIDs.delete(id)
  }
}

async function saveSelectedName() {
  const agent = selectedAgent.value
  const name = selectedName.value.trim()
  if (!agent || !name || name === botAgentName(agent)) return
  try {
    await updateAgent({ agent, body: { name } })
  } catch (error) {
    selectedName.value = botAgentName(agent)
    toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
  }
}

async function confirmDelete() {
  const agent = deleteTarget.value
  if (!agent?.id || deleting.value) return
  deleting.value = true
  try {
    await deleteBotsByBotIdAgentsById({
      path: { bot_id: props.botId, id: agent.id },
      throwOnError: true,
    })
    deleteTarget.value = null
    if (selectedID.value === agent.id) closeDetail()
    toast.success(t('bots.agent.deleted'))
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.agent.deleteFailed')))
  } finally {
    deleting.value = false
    void queryCache.invalidateQueries({ key: ['bot-agents', props.botId] })
    void queryCache.invalidateQueries({ key: ['bot-settings', props.botId] })
  }
}

async function persistACPForm() {
  if (!bot.value) return
  if (persistRunning.value) {
    persistQueued.value = true
    return
  }
  const normalized = normalizeACPForm(form, profiles.value)
  // Shared ACP credentials outlive individual BotAgent rows. Never turn the
  // legacy provider bit off when one configured instance is disabled or
  // deleted. New, incomplete Agents stay disabled until their required setup
  // is present so partial settings can be saved without failing server checks.
  for (const agent of agents.value) {
    const provider = botAgentProvider(agent)
    const profile = profileFor(agent)
    const config = provider ? normalized.agents[provider] : undefined
    if (profile && config && !findMissingRequiredManagedField(profile, config.managed, config.setup_mode)) {
      config.enabled = true
    }
  }
  const snapshot = JSON.stringify(normalized)
  if (snapshot === lastPersistedSnapshot.value) return
  persistRunning.value = true
  try {
    await updateBot({ metadata: withACPMetadata(botMetadata.value, normalized, profiles.value) })
    lastPersistedSnapshot.value = snapshot
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
    if (!persistQueued.value) applyMetadataToForm(botMetadata.value, profiles.value, true)
  } finally {
    persistRunning.value = false
    if (persistQueued.value) {
      persistQueued.value = false
      void persistACPForm()
    }
  }
}

function closeDetail() {
  backToList()
}

function applyMetadataToForm(metadata: Record<string, unknown> | undefined, list: AcpprofilePublicProfile[], force = false) {
  const next = readACPConfig(metadata, list)
  const nextSnapshot = JSON.stringify(next)
  const currentSnapshot = JSON.stringify(normalizeACPForm(form, list))
  if (!force && (persistRunning.value || persistQueued.value || currentSnapshot !== lastPersistedSnapshot.value) && nextSnapshot === lastPersistedSnapshot.value) return
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
