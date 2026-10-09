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
            <span
              v-if="checkingAgentID === agent.id && enableFlow?.checking"
              class="flex items-center gap-1.5 text-caption text-muted-foreground"
            >
              <Spinner class="size-3" />
              {{ t('bots.agent.dependencyChecking') }}
            </span>

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
            v-if="selectedAgent && selectedProfile && selectedSetup"
            :key="`${botId}:${selectedAgent.id}:${selectedProfile.id}`"
            :bot-id="botId"
            :profile="selectedProfile"
            :setup="selectedSetup"
            @commit="persistSetup(selectedAgent)"
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
  <DependencyEnableFlow
    ref="enableFlow"
    :bot-id="botId"
  />
  <AddBotAgentDialog
    v-model:open="addOpen"
    :bot-id="botId"
    :profiles="profiles"
    :agents="agents"
    @created="onAgentCreated"
  />
</template>

<script setup lang="ts">
import { normalizeAgentID } from '@/utils/external-agent'
import { computed, nextTick, reactive, ref, useTemplateRef, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
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
  Spinner,
  SwapTransition,
  Switch,
  toast,
} from '@felinic/ui'
import { MoreHorizontal, Plus, Trash2 } from 'lucide-vue-next'
import { SettingsIcon as Settings } from '@memohai/icon/ui'
import {
  deleteBotsByBotIdAgentsById,
  getAcpProfiles,
  getBotsByBotIdAgents,
  patchBotsByBotIdAgentsById,
  type AcpprofilePublicProfile,
  type BotagentsBotAgent,
} from '@memohai/sdk'
import type { Ref } from 'vue'
import SettingsAcpDetail from './settings-acp-detail.vue'
import SettingsDirectAgentDetail from './settings-direct-agent-detail.vue'
import AddBotAgentDialog from './add-bot-agent-dialog.vue'
import DependencyEnableFlow from './dependency-enable-flow.vue'
import { agentDependencyRequirement } from './dependency-enable-flow'
import { externalAgentModelsQueryKey } from '@/composables/useAgentModelCatalog'
import { useViewSwap } from '@/composables/useViewSwap'
import { resolveApiErrorMessage } from '@/utils/api-error'
import {
  findMissingRequiredManagedField,
  readACPAgentForm,
  withACPAgentForm,
  type ACPAgentForm,
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

const props = defineProps<{ botId: string }>()
const { t } = useI18n()
const queryCache = useQueryCache()
const botIdRef = computed(() => props.botId) as Ref<string>

// Editable copy of each ACP agent's own setup, keyed by agent id: every
// custom ACP agent shares the one generic profile, so the profile id cannot
// tell two of them apart. savedSetups holds what the server last confirmed.
const setups = reactive<Record<string, ACPAgentForm>>({})
const savedSetups = new Map<string, string>()
const busyAgentIDs = reactive(new Set<string>())
const enableFlow = useTemplateRef<InstanceType<typeof DependencyEnableFlow>>('enableFlow')
const checkingAgentID = ref('')
const addOpen = ref(false)
const deleteTarget = ref<BotagentsBotAgent | null>(null)
const deleting = ref(false)

const { view, direction, openDetail, backToList } = useViewSwap()
const selectedID = ref('')
const selectedName = ref('')

const route = useRoute()
const router = useRouter()
const addRequested = computed(() => route.name === 'bot-detail'
  && route.query.tab === 'agents'
  && route.query.addAgent === props.botId)
watch(addRequested, async (requested) => {
  if (!requested) return
  // Wait for the existing dialog to mount, including when this tab was cached.
  await nextTick()
  if (!addRequested.value) return
  addOpen.value = true
  const query = { ...route.query }
  delete query.addAgent
  await router.replace({ query })
}, { immediate: true, flush: 'post' })

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

const selectedAgent = computed(() => agents.value.find(agent => agent.id === selectedID.value) ?? null)
const selectedProfile = computed(() => {
  const provider = botAgentProvider(selectedAgent.value)
  return profiles.value.find(profile => normalizeAgentID(profile.id) === provider) ?? null
})
const selectedDirectRuntime = computed(() => {
  const runtime = normalizeBotAgentRuntime(selectedAgent.value?.runtime)
  return runtime === BOT_AGENT_RUNTIME_CODEX || runtime === BOT_AGENT_RUNTIME_CLAUDE_CODE ? runtime : ''
})

const selectedSetup = computed(() => setups[selectedID.value] ?? null)

const { mutateAsync: updateAgent } = useMutation({
  mutation: async ({ agent, body }: { agent: BotagentsBotAgent; body: { name?: string; enabled?: boolean; metadata?: Record<string, unknown> } }) => {
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

watch([agents, profiles], () => syncSetups(), { immediate: true })

watch(selectedAgent, (agent) => {
  selectedName.value = botAgentName(agent)
})

watch(agents, (list) => {
  if (view.value === 'detail' && selectedID.value && !list.some(agent => agent.id === selectedID.value)) closeDetail()
})

function profileFor(agent: BotagentsBotAgent): AcpprofilePublicProfile | null {
  const provider = botAgentProvider(agent)
  return profiles.value.find(profile => normalizeAgentID(profile.id) === provider) ?? null
}

function agentNeedsConfig(agent: BotagentsBotAgent): boolean {
  const directConfigured = isDirectBotAgentConfigured(agent)
  if (directConfigured !== null) return !directConfigured
  const profile = profileFor(agent)
  if (!profile) return true
  const config = setups[agent.id ?? ''] ?? readACPAgentForm(agent, profile)
  if (config.setup_mode === 'self') return false
  return findMissingRequiredManagedField(profile, config.managed, config.setup_mode) !== null
}

function openAgent(agent: BotagentsBotAgent) {
  if (!agent.id) return
  selectedID.value = agent.id
  selectedName.value = botAgentName(agent)
  openDetail()
}

const agentDependency = agentDependencyRequirement

// Blocking preflight before an agent goes live. Resolves true
// when the agent declares no dependency or the flow ended with it satisfied;
// the Switch stays bound to the server value, so nothing lights up until
// `enabled: true` is actually written.
async function ensureAgentDependency(agent: BotagentsBotAgent): Promise<boolean> {
  if (!agentDependency(agent)) return true
  const flow = enableFlow.value
  if (!flow) return false
  checkingAgentID.value = agent.id ?? ''
  try {
    return await flow.run(agent)
  } finally {
    checkingAgentID.value = ''
  }
}

async function setAgentEnabled(agent: BotagentsBotAgent, enabled: boolean) {
  const id = agent.id ?? ''
  if (!id || busyAgentIDs.has(id)) return
  busyAgentIDs.add(id)
  try {
    // A cancelled or failed preflight must not change the enabled state.
    if (enabled && !(await ensureAgentDependency(agent))) return
    await updateAgent({ agent, body: { enabled } })
    if (enabled && agentNeedsConfig(agent)) openAgent(agent)
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
  } finally {
    busyAgentIDs.delete(id)
  }
}

// A direct agent is created disabled; it is enabled here once its dependency
// checks out, and the detail opens either way so credentials can be set up.
async function onAgentCreated(agent: BotagentsBotAgent) {
  const id = agent.id ?? ''
  if (id && agent.enabled === false && agentDependency(agent) && !busyAgentIDs.has(id)) {
    busyAgentIDs.add(id)
    try {
      if (await ensureAgentDependency(agent)) await updateAgent({ agent, body: { enabled: true } })
    } catch (error) {
      toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
    } finally {
      busyAgentIDs.delete(id)
    }
  }
  openAgent(agent)
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

// Saves run one at a time and each reads the setup when it starts, so two
// field commits in quick succession cannot land out of order and leave an
// older value on the server.
let setupSaveChain: Promise<unknown> = Promise.resolve()
function persistSetup(agent: BotagentsBotAgent) {
  const run = setupSaveChain.then(() => saveSetup(agent))
  setupSaveChain = run.catch(() => undefined)
  return run
}

async function saveSetup(agent: BotagentsBotAgent) {
  const id = agent.id ?? ''
  const setup = setups[id]
  if (!id || !setup) return
  const snapshot = JSON.stringify(setup)
  if (snapshot === savedSetups.get(id)) return
  try {
    await updateAgent({ agent, body: { metadata: withACPAgentForm(agent, setup) } })
    savedSetups.set(id, snapshot)
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
    savedSetups.delete(id)
    syncSetups()
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

// Every save refetches the agent list. An edit the user has not committed yet
// must survive that refetch, so a setup is only replaced when the server copy
// actually moved away from what was last saved.
function syncSetups() {
  const live = new Set(agents.value.map(agent => agent.id ?? ''))
  for (const id of Object.keys(setups)) {
    if (live.has(id)) continue
    delete setups[id]
    savedSetups.delete(id)
  }
  for (const agent of agents.value) {
    const id = agent.id ?? ''
    const profile = profileFor(agent)
    if (!id || !profile) continue
    const server = readACPAgentForm(agent, profile)
    const serverSnapshot = JSON.stringify(server)
    const current = setups[id]
    if (current && serverSnapshot === savedSetups.get(id)) continue
    setups[id] = server
    savedSetups.set(id, serverSnapshot)
  }
}
</script>
