<template>
  <SwapTransition :direction="direction">
    <PageShell
      v-if="view === 'list'"
      variant="tab"
      :title="t('bots.tabs.agents')"
    >
      <template #actions>
        <Button @click="addOpen = true">
          <Plus />
          {{ t('bots.agent.add') }}
        </Button>
      </template>

      <SettingsSection v-if="agentsLoading && agents.length === 0">
        <SettingsRow
          v-for="n in 2"
          :key="n"
        >
          <template #leading>
            <Skeleton class="size-8 rounded-full" />
          </template>
          <template #content>
            <div class="space-y-2">
              <Skeleton class="h-4 w-32" />
              <Skeleton class="h-3 w-20" />
            </div>
          </template>
          <Skeleton class="h-5 w-9 rounded-full" />
        </SettingsRow>
      </SettingsSection>

      <SettingsSection v-else-if="agents.length === 0">
        <div class="flex flex-col items-center justify-center gap-4 px-4 py-12 text-center">
          <div>
            <p class="text-control font-medium text-foreground">
              {{ t('bots.agent.emptyTitle') }}
            </p>
            <p class="mt-1 text-body text-muted-foreground">
              {{ t('bots.agent.emptyDescription') }}
            </p>
          </div>
          <Button
            variant="outline"
            @click="addOpen = true"
          >
            <Plus />
            {{ t('bots.agent.add') }}
          </Button>
        </div>
      </SettingsSection>

      <SettingsSection v-else>
        <SettingsRow
          v-for="agent in agents"
          :key="agent.id"
          :label="botAgentName(agent)"
          :description="providerLabel(agent)"
        >
          <template #leading>
            <span class="flex size-9 items-center justify-center">
              <component
                :is="botAgentIcon(agent, true)"
                class="size-5"
              />
            </span>
          </template>

          <div class="flex items-center gap-2">
            <Badge
              v-if="agent.enabled !== false && agentNeedsConfig(agent)"
              variant="warning"
              size="sm"
            >
              {{ t('bots.agent.statusNeedsConfig') }}
            </Badge>

            <Button
              variant="ghost"
              size="icon-sm"
              :aria-label="t('common.edit')"
              @click="openAgent(agent)"
            >
              <Settings />
            </Button>

            <DropdownMenu>
              <DropdownMenuTrigger as-child>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  :aria-label="t('common.actions')"
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

            <Switch
              :model-value="agent.enabled !== false"
              :disabled="busyAgentIDs.has(agent.id ?? '')"
              :aria-label="botAgentName(agent)"
              @update:model-value="(value) => setAgentEnabled(agent, !!value)"
            />
          </div>
        </SettingsRow>
      </SettingsSection>

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

    <DetailPane
      v-else
      width="narrow"
      :back-label="t('bots.tabs.agents')"
      @back="closeDetail"
    >
      <SettingsShell width="narrow">
        <div class="space-y-8">
          <SettingsSection v-if="selectedAgent">
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

          <SettingsDirectAgentDetail
            v-else-if="selectedAgent && selectedDirectRuntime"
            :key="`${botId}:${selectedAgent.id}:${selectedDirectRuntime}`"
            :bot-id="botId"
            :agent="selectedAgent"
            @authorized="refreshDirectRuntimeModels"
          />
        </div>
      </SettingsShell>
    </DetailPane>
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
  DetailPane,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
  Input,
  PageShell,
  SettingsRow,
  SettingsSection,
  SettingsShell,
  Skeleton,
  SwapTransition,
  Switch,
  toast,
} from '@felinic/ui'
import { MoreHorizontal, Plus, Settings, Trash2 } from 'lucide-vue-next'
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
import SettingsDirectAgentDetail from './settings-direct-agent-detail.vue'
import AddBotAgentDialog from './add-bot-agent-dialog.vue'
import { externalAgentModelsQueryKey } from '@/composables/useAgentModelCatalog'
import { useViewSwap } from '@/composables/useViewSwap'
import { resolveApiErrorMessage } from '@/utils/api-error'
import {
  acpAgentDisplayName,
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
import {
  BOT_AGENT_RUNTIME_CLAUDE_CODE,
  BOT_AGENT_RUNTIME_CODEX,
  botAgentIcon,
  botAgentName,
  botAgentProvider,
  isDirectBotAgentConfigured,
  normalizeBotAgentRuntime,
} from '@/utils/bot-agent'
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
const selectedDirectRuntime = computed(() => {
  const runtime = normalizeBotAgentRuntime(selectedAgent.value?.runtime)
  return runtime === BOT_AGENT_RUNTIME_CODEX || runtime === BOT_AGENT_RUNTIME_CLAUDE_CODE ? runtime : ''
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
  onSuccess: (data) => {
    // Both save paths compose the full metadata tree from botMetadata; write
    // the server's result back immediately so the next save composes on the
    // fresh tree instead of a stale snapshot (invalidation refetch is async).
    if (data) queryCache.setQueryData(['bot', props.botId], data)
  },
  onSettled: () => {
    void queryCache.invalidateQueries({ key: ['bot', props.botId] })
    void queryCache.invalidateQueries({ key: getBotsQueryKey() })
    void chatStore.refreshBots().catch(() => {})
  },
})

// Serializes every bot-metadata save on this page: the ACP form and the
// direct-agent panel each write the whole metadata tree, so two concurrent
// PUTs would overwrite each other's subtree with a stale snapshot.
let botMetadataSaveChain: Promise<unknown> = Promise.resolve()
function withBotMetadataSaveLock<T>(task: () => Promise<T>): Promise<T> {
  const run = botMetadataSaveChain.then(task, task)
  botMetadataSaveChain = run.catch(() => undefined)
  return run
}

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
  const provider = botAgentProvider(agent)
  return profile?.display_name?.trim() || acpAgentDisplayName(provider, provider)
}

function agentForm(profile: AcpprofilePublicProfile): ACPAgentForm {
  return ensureACPAgentForm(form, profile)
}

function agentNeedsConfig(agent: BotagentsBotAgent): boolean {
  const directConfigured = isDirectBotAgentConfigured(agent)
  if (directConfigured !== null) return !directConfigured
  const profile = profileFor(agent)
  if (!profile) return true
  const config = agentForm(profile)
  if (config.setup_mode === 'self') return false
  return findMissingRequiredManagedField(profile, config.managed, config.setup_mode) !== null
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
    await withBotMetadataSaveLock(() => updateBot({ metadata: withACPMetadata(botMetadata.value, normalized, profiles.value) }))
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

function refreshDirectRuntimeModels() {
  const runtime = selectedDirectRuntime.value
  const agentID = selectedAgent.value?.id
  if (!runtime || !agentID) return
  void queryCache.invalidateQueries({ key: externalAgentModelsQueryKey(runtime, props.botId, agentID) })
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
