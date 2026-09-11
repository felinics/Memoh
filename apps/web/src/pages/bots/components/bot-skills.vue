<template>
  <PageShell
    variant="tab"
    :title="$t('bots.skills.title')"
    :description="$t('bots.skills.intro')"
  >
    <template #actions>
      <Button
        v-if="!selectedApp"
        variant="outline"
        size="sm"
        @click="isDiscoveryDialogOpen = true"
      >
        <SlidersHorizontal class="size-4" />
        {{ $t('bots.skills.discoveryTitle') }}
        <Badge
          v-if="showDiscoveryIndicator"
          variant="default"
          size="sm"
        >
          {{ $t('bots.skills.discoverySummaryUnsaved') }}
        </Badge>
      </Button>
      <Button
        v-if="!selectedApp"
        size="sm"
        @click="handleCreate"
      >
        <Plus class="size-4" />
        {{ $t('bots.skills.addSkill') }}
      </Button>
      <Button
        v-if="selectedApp"
        variant="outline"
        size="sm"
        @click="openAppsTab"
      >
        <Box class="size-4" />
        {{ $t('bots.skills.manageApp') }}
      </Button>
    </template>

    <Button
      v-if="selectedApp"
      variant="ghost"
      size="sm"
      class="mb-2 w-fit"
      @click="closeApp"
    >
      <ArrowLeft class="size-4" />
      {{ $t('bots.skills.backToLibrary') }}
    </Button>

    <SettingsSection :title="selectedApp?.appId || $t('bots.skills.libraryTitle')">
      <!-- Loading borrows the skill-row height to hold the list's space steady
           (no CLS) until skills load — same card-row family as the row list it
           stands in for. -->
      <InlineLoadingRow
        v-if="isLoading"
        size="md"
        surface="card-row"
      >
        {{ $t('common.loading') }}
      </InlineLoadingRow>

      <Empty
        v-else-if="appLoadFailed"
        class="py-12"
      >
        <EmptyHeader>
          <EmptyTitle>{{ $t('bots.skills.loadFailed') }}</EmptyTitle>
        </EmptyHeader>
      </Empty>

      <Empty
        v-else-if="!skills.length && !apps.length"
        class="py-12"
      >
        <EmptyHeader>
          <EmptyTitle>{{ $t('bots.skills.emptyTitle') }}</EmptyTitle>
          <EmptyDescription>{{ $t('bots.skills.emptyDescription') }}</EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <div class="flex items-center gap-2">
            <Button
              size="sm"
              @click="handleCreate"
            >
              <Plus class="size-4" />
              {{ $t('bots.skills.addSkill') }}
            </Button>
            <Button
              variant="outline"
              size="sm"
              @click="isDiscoveryDialogOpen = true"
            >
              <SlidersHorizontal class="size-4" />
              {{ $t('bots.skills.discoveryTitle') }}
            </Button>
          </div>
        </EmptyContent>
      </Empty>

      <template v-else>
        <template v-if="selectedApp">
          <Empty
            v-if="!selectedApp.skills.length"
            class="py-12"
          >
            <EmptyHeader>
              <EmptyTitle>{{ $t('bots.skills.appSkillsUnavailable') }}</EmptyTitle>
            </EmptyHeader>
          </Empty>
          <SettingsRow
            v-for="skill in selectedApp.skills"
            :key="skillKey(skill)"
            align="start"
          >
            <template #content>
              <div class="flex min-w-0 items-center gap-2">
                <h3
                  class="truncate font-mono text-sm font-medium text-foreground"
                  :class="{ 'line-through text-muted-foreground': skill.state === 'shadowed' }"
                  :title="skill.name"
                >
                  {{ skill.name }}
                </h3>
                <Badge
                  variant="outline"
                  size="sm"
                >
                  {{ skillStateLabel(skill) }}
                </Badge>
              </div>
              <p
                class="mt-1 line-clamp-2 break-words text-xs text-muted-foreground [overflow-wrap:anywhere]"
                :title="skill.description"
              >
                {{ skill.description || '-' }}
              </p>
            </template>
            <Button
              variant="ghost"
              size="icon-sm"
              :aria-label="$t('bots.skills.viewSkill')"
              @click="handleView(skill)"
            >
              <Eye class="size-3.5" />
            </Button>
          </SettingsRow>
        </template>

        <template v-else>
          <SettingsRow
            v-for="pkg in apps"
            :key="pkg.key"
            align="start"
            :class="appRowClass"
            role="button"
            tabindex="0"
            @click="openApp(pkg.key)"
            @keydown.enter.prevent="openApp(pkg.key)"
            @keydown.space.prevent="openApp(pkg.key)"
          >
            <template #content>
              <div class="flex min-w-0 items-center gap-2">
                <Box class="size-4 shrink-0 text-muted-foreground" />
                <h3
                  class="truncate font-mono text-sm font-medium text-foreground"
                  :title="pkg.appId"
                >
                  {{ pkg.appId }}
                </h3>
                <Badge
                  variant="outline"
                  size="sm"
                >
                  {{ $t('bots.skills.appBadge') }}
                </Badge>
              </div>
              <p class="mt-2 truncate font-mono text-xs text-muted-foreground">
                {{ pkg.registryId }}
              </p>
            </template>
            <ChevronRight
              class="size-3.5 text-muted-foreground"
              aria-hidden="true"
            />
          </SettingsRow>

          <SettingsRow
            v-for="skill in standaloneSkills"
            :key="skillKey(skill)"
            align="start"
          >
            <template #content>
              <div class="flex min-w-0 items-center gap-2">
                <h3
                  class="truncate font-mono text-sm font-medium text-foreground"
                  :class="{ 'line-through text-muted-foreground': skill.state === 'shadowed' }"
                  :title="skill.name"
                >
                  {{ skill.name }}
                </h3>
                <Badge
                  variant="outline"
                  size="sm"
                >
                  {{ skillStateLabel(skill) }}
                </Badge>
                <Badge
                  variant="default"
                  size="sm"
                >
                  {{ skill.managed ? $t('bots.skills.managedBadge') : $t('bots.skills.discoveredBadge') }}
                </Badge>
              </div>
              <p
                class="mt-1 line-clamp-2 break-words text-xs text-muted-foreground [overflow-wrap:anywhere]"
                :title="skill.description"
              >
                {{ skill.description || '-' }}
              </p>
              <p
                class="mt-2 truncate font-mono text-xs text-muted-foreground"
                :title="sourceSummary(skill)"
              >
                {{ sourceSummary(skill) }}
              </p>
              <p
                v-if="skill.state === 'shadowed'"
                class="mt-3 text-xs text-muted-foreground"
              >
                {{ $t('bots.skills.shadowedHint') }}
              </p>
            </template>

            <div class="flex items-center gap-1">
              <Button
                v-if="skill.editable"
                variant="ghost"
                size="icon-sm"
                :aria-label="!skill.managed ? $t('bots.skills.overrideTitle') : $t('common.edit')"
                @click="handleEdit(skill)"
              >
                <SquarePen class="size-3.5" />
              </Button>

              <Button
                v-if="skill.state === 'disabled'"
                variant="ghost"
                size="icon-sm"
                loading-mode="icon"
                :loading="isSkillActionPending(skill, 'enable')"
                :disabled="isActioning"
                :aria-label="$t('bots.skills.enableAction')"
                @click="handleSkillAction('enable', skill)"
              >
                <EyeOff class="size-3.5" />
              </Button>
              <Button
                v-else
                variant="ghost"
                size="icon-sm"
                loading-mode="icon"
                :loading="isSkillActionPending(skill, 'disable')"
                :disabled="isActioning"
                :aria-label="$t('bots.skills.disableAction')"
                @click="handleSkillAction('disable', skill)"
              >
                <Eye class="size-3.5" />
              </Button>

              <Button
                v-if="!skill.managed"
                variant="ghost"
                size="icon-sm"
                loading-mode="icon"
                :loading="isSkillActionPending(skill, 'adopt')"
                :disabled="isActioning || skill.state === 'shadowed'"
                :aria-label="skill.state === 'shadowed' ? $t('bots.skills.adoptBlocked') : $t('bots.skills.adoptAction')"
                @click="handleSkillAction('adopt', skill)"
              >
                <ArrowDownToLine class="size-3.5" />
              </Button>

              <ConfirmPopover
                v-if="skill.deletable"
                :message="$t('bots.skills.deleteConfirm')"
                :cancel-text="$t('common.cancel')"
                :confirm-text="$t('common.confirm')"
                :loading="isDeleting && deletingPath === skill.source_path"
                @confirm="handleDelete(skill)"
              >
                <template #trigger>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    :disabled="isDeleting"
                    :aria-label="$t('common.delete')"
                  >
                    <Trash2 class="size-3.5" />
                  </Button>
                </template>
              </ConfirmPopover>
            </div>
          </SettingsRow>
        </template>
      </template>
    </SettingsSection>

    <!-- Edit Dialog (Modal IDE) -->
    <Dialog v-model:open="isDialogOpen">
      <DialogContent class="flex max-h-[calc(100vh-2rem)] flex-col overflow-hidden p-0 sm:h-[85vh] sm:max-w-4xl">
        <DialogHeader class="shrink-0 border-b border-border p-4">
          <DialogTitle class="text-sm font-semibold">
            {{ isViewing ? $t('bots.skills.viewSkill') : isEditing ? $t('common.edit') : $t('bots.skills.addSkill') }}
          </DialogTitle>
        </DialogHeader>
        
        <div class="min-h-0 flex-1 p-4">
          <div class="flex h-full min-h-0 flex-col overflow-hidden rounded-[var(--radius-menu-shell)] border border-border">
            <MonacoEditor
              v-model="draftRaw"
              language="markdown"
              :readonly="isViewing || isSaving"
              class="min-h-0 flex-1"
              :options="{
                automaticLayout: true,
                fixedOverflowWidgets: true,
                minimap: { enabled: false },
                scrollBeyondLastLine: false
              }"
            />
          </div>
        </div>

        <DialogFooter class="shrink-0 items-center gap-2 border-t border-border p-4">
          <DialogClose as-child>
            <Button
              variant="ghost"
              size="sm"
              :disabled="isSaving"
            >
              {{ isViewing ? $t('bots.skills.close') : $t('common.cancel') }}
            </Button>
          </DialogClose>
          <Button
            v-if="!isViewing"
            size="sm"
            class="min-w-24"
            :disabled="!canSave"
            :loading="isSaving"
            @click="handleSave"
          >
            {{ $t('common.confirm') }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <!-- Discovery Modal -->
    <Dialog v-model:open="isDiscoveryDialogOpen">
      <DialogContent class="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle class="text-sm font-semibold">
            {{ $t('bots.skills.discoveryTitle') }}
          </DialogTitle>
          <DialogDescription class="text-xs">
            {{ $t('bots.skills.discoveryDescription') }}
          </DialogDescription>
        </DialogHeader>

        <!-- py-3 is the dialog-body inset; the field-run rhythm itself is owned by
             FormStack so the field→field gap matches every other house form. -->
        <div class="py-3">
          <FormStack>
            <FieldStack>
              <!-- Custom label markup preserved via the #label slot to keep its exact
                   size/weight/color; the read-only managed path is not an editable
                   control, so it has no `for` binding. -->
              <template #label>
                <Label class="text-xs font-medium text-foreground">
                  {{ $t('bots.skills.managedPathLabel') }}
                </Label>
              </template>
              <div class="break-all rounded-md border border-border px-2.5 py-1.5 font-mono text-xs text-muted-foreground">
                {{ MANAGED_SKILL_PATH }}
              </div>
              <p class="text-xs text-muted-foreground">
                {{ $t('bots.skills.managedPathHint') }}
              </p>
            </FieldStack>

            <FieldStack>
              <template #label>
                <Label class="text-xs font-medium text-foreground">
                  {{ $t('bots.skills.discoveryPathsLabel') }}
                </Label>
              </template>
              <Textarea
                v-model="discoveryRootsDraft"
                :disabled="discoveryControlsDisabled"
                :placeholder="$t('bots.skills.discoveryPathPlaceholder')"
                class="min-h-24 font-mono text-xs"
                :aria-invalid="hasDiscoveryRootErrors"
              />
              <!-- Help lives in the default slot, not the `help` prop, because it
                   switches between a destructive error and the muted default hint. -->
              <p
                v-if="discoveryRootError"
                class="text-xs text-destructive"
              >
                {{ discoveryRootError }}
              </p>
              <p
                v-else
                class="text-xs text-muted-foreground"
              >
                {{ $t('bots.skills.discoveryDefaultHint', { paths: DEFAULT_DISCOVERY_ROOTS.join(', ') }) }}
              </p>
            </FieldStack>
          </FormStack>
        </div>

        <DialogFooter class="gap-2 sm:space-x-0">
          <Button
            variant="ghost"
            size="sm"
            :disabled="discoveryControlsDisabled || !isDiscoveryRootsDirty"
            @click="resetDiscoveryRoots"
          >
            {{ $t('bots.skills.discoveryReset') }}
          </Button>
          <div class="flex items-center gap-2">
            <Button
              variant="ghost"
              size="sm"
              :disabled="isSavingDiscoveryRoots"
              @click="closeDiscoveryDialog"
            >
              {{ $t('common.cancel') }}
            </Button>
            <Button
              size="sm"
              class="min-w-24"
              :disabled="!canSaveDiscoveryRoots"
              :loading="isSavingDiscoveryRoots"
              @click="handleSaveDiscoveryRoots"
            >
              {{ $t('common.confirm') }}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </PageShell>
</template>

<script setup lang="ts">
import { ArrowDownToLine, ArrowLeft, Box, ChevronRight, Eye, EyeOff, Plus, SlidersHorizontal, SquarePen, Trash2 } from 'lucide-vue-next'
import { computed, onActivated, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ConfirmPopover, FieldStack, FormStack, InlineLoadingRow, PageShell, SettingsRow, SettingsSection, toast } from '@felinic/ui'
import { useQuery, useQueryCache } from '@pinia/colada'
import {
  Badge,
  Button,
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter, DialogClose,
  Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyTitle,
  Label, Textarea,
} from '@felinic/ui'
import MonacoEditor from '@/components/monaco-editor/index.vue'
import {
  getBotsById,
  getBotsByBotIdContainerSkills,
  getBotsByBotIdApps,
  getBotsByBotIdWorkspaceTargets,
  postBotsByBotIdContainerSkills,
  postBotsByBotIdContainerSkillsActions,
  deleteBotsByBotIdContainerSkills,
  putBotsById,
  type HandlersSkillItem,
  type HandlersAppItem,
} from '@memohai/sdk'
import { getBotsQueryKey } from '@memohai/sdk/colada'
import { useRoute, useRouter } from 'vue-router'
import { resolveApiErrorMessage } from '@/utils/api-error'

type SkillItem = HandlersSkillItem & {
  source_path?: string
  source_root?: string
  source_kind?: string
  managed?: boolean
  state?: string
  shadowed_by?: string
  registry_id?: string
  app_id?: string
  skill_id?: string
}

type App = {
  key: string
  installationId: string
  registryId: string
  appId: string
  workspaceTargetId: string
  revision: string
  skills: SkillItem[]
}

// The whole SettingsRow is the App navigation target, so this page owns its interaction state.
const appRowClass = 'cursor-pointer transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset' /* ui-allow-style */

const props = defineProps<{
  botId: string
}>()

const { t } = useI18n()
const queryCache = useQueryCache()
const route = useRoute()
const router = useRouter()

const MANAGED_SKILL_PATH = '/data/skills'
const DEFAULT_DISCOVERY_ROOTS = ['/data/.agents/skills', '/root/.agents/skills']
const RESERVED_DISCOVERY_ROOTS = new Set(['/data/skills', '/data/.skills'])
const WORKSPACE_METADATA_KEY = 'workspace'
const SKILL_DISCOVERY_ROOTS_METADATA_KEY = 'skill_discovery_roots'

const isLoading = ref(false)
const isSaving = ref(false)
const isDeleting = ref(false)
const deletingPath = ref('')
const isActioning = ref(false)
const actionTargetPath = ref('')
const actionName = ref('')
const skills = ref<SkillItem[]>([])
const installedApps = ref<HandlersAppItem[]>([])
const appLoadFailed = ref(false)
const isSavingDiscoveryRoots = ref(false)
const isDiscoveryDialogOpen = ref(false)
const discoveryRootsDraft = ref(DEFAULT_DISCOVERY_ROOTS.join('\n'))
const savedDiscoveryRoots = ref<string[]>([...DEFAULT_DISCOVERY_ROOTS])

const isDialogOpen = ref(false)
const isEditing = ref(false)
const isViewing = ref(false)
const draftRaw = ref('')
const editingSourcePath = ref('')
const selectedAppKey = ref('')
let skillsLoadSequence = 0
let appsLoadSequence = 0
let libraryLoadSequence = 0

const SKILL_TEMPLATE = `---
name: my-skill
description: Brief description
---

# My Skill
`

const canSave = computed(() => {
  return !isViewing.value && draftRaw.value.trim().length > 0
})

const apps = computed<App[]>(() => {
  const skillsByApp = new Map<string, SkillItem[]>()
  for (const skill of skills.value) {
    if (!skill.registry_id || !skill.app_id) continue
    const key = `${skill.registry_id}/${skill.app_id}`
    const members = skillsByApp.get(key) || []
    members.push(skill)
    skillsByApp.set(key, members)
  }
  return installedApps.value
    .filter(item => item.installation_id && item.registry_id && item.app_id && (item.skills?.length || skillsByApp.has(`${item.registry_id}/${item.app_id}`)))
    .map(item => {
      const identity = `${item.registry_id}/${item.app_id}`
      return {
        key: `${installedAppsTargetId.value}:${identity}`,
        installationId: item.installation_id ?? '',
        registryId: item.registry_id ?? '',
        appId: item.app_id ?? '',
        workspaceTargetId: installedAppsTargetId.value,
        revision: item.revision ?? '',
        skills: skillsByApp.get(identity) || [],
      }
    })
    .sort((left, right) => left.appId.localeCompare(right.appId))
})
const installedAppsTargetId = ref('')
const installedAppIdentities = computed(() => new Set(
  installedApps.value
    .filter(item => item.installation_id)
    .map(item => `${item.registry_id}/${item.app_id}`),
))
const standaloneSkills = computed(() => skills.value.filter((skill) => {
  if (!skill.registry_id || !skill.app_id) return true
  if (appLoadFailed.value) return false
  return !installedAppIdentities.value.has(`${skill.registry_id}/${skill.app_id}`)
}))
const selectedApp = computed(() => apps.value.find(pkg => pkg.key === selectedAppKey.value) || null)

const { data: bot, refetch: refetchBot } = useQuery({
  key: () => ['bot', props.botId],
  query: async () => {
    const { data } = await getBotsById({ path: { id: props.botId }, throwOnError: true })
    return data
  },
  enabled: () => !!props.botId,
})

const discoveryRootErrors = computed(() => validateDiscoveryRoots(discoveryRootsDraft.value))
const discoveryRootError = computed(() => discoveryRootErrors.value[0] || '')
const hasDiscoveryRootErrors = computed(() => discoveryRootErrors.value.length > 0)
const normalizedDiscoveryRootDrafts = computed(() => normalizeDiscoveryRoots(parseDiscoveryRoots(discoveryRootsDraft.value)))
const isDiscoveryRootsDirty = computed(() => !areStringListsEqual(normalizedDiscoveryRootDrafts.value, savedDiscoveryRoots.value))
const savedDiscoveryRootsText = computed(() => savedDiscoveryRoots.value.join('\n'))
const isDiscoveryDraftModified = computed(() => discoveryRootsDraft.value !== savedDiscoveryRootsText.value)
const usesDefaultDiscoveryRoots = computed(() => areStringListsEqual(savedDiscoveryRoots.value, DEFAULT_DISCOVERY_ROOTS))
const showDiscoveryIndicator = computed(() => !usesDefaultDiscoveryRoots.value || isDiscoveryRootsDirty.value)
const discoveryControlsDisabled = computed(() => isSavingDiscoveryRoots.value || !bot.value)
const canSaveDiscoveryRoots = computed(() => {
  return !!bot.value && isDiscoveryRootsDirty.value && !hasDiscoveryRootErrors.value && !isSavingDiscoveryRoots.value
})

async function fetchSkills(workspaceTargetId = '') {
  if (!props.botId) return
  const botID = props.botId
  const sequence = ++skillsLoadSequence
  try {
    const { data } = await getBotsByBotIdContainerSkills({
      path: { bot_id: botID },
      query: workspaceTargetId ? { workspace_target_id: workspaceTargetId } : undefined,
      throwOnError: true,
    })
    if (props.botId !== botID || sequence !== skillsLoadSequence) return
    skills.value = data.skills || []
  } catch (error) {
    if (props.botId !== botID || sequence !== skillsLoadSequence) return
    skills.value = []
    toast.error(resolveApiErrorMessage(error, t('bots.skills.loadFailed')))
  }
}

async function fetchInstalledApps(workspaceTargetId = '') {
  if (!props.botId) return
  const botID = props.botId
  const sequence = ++appsLoadSequence
  try {
    const { data } = await getBotsByBotIdApps({
      path: { bot_id: botID },
      query: workspaceTargetId ? { workspace_target_id: workspaceTargetId } : undefined,
      throwOnError: true,
    })
    if (props.botId !== botID || sequence !== appsLoadSequence) return
    installedApps.value = data.items || []
    installedAppsTargetId.value = data.workspace_target_id || workspaceTargetId
    appLoadFailed.value = false
  } catch (error) {
    if (props.botId !== botID || sequence !== appsLoadSequence) return
    installedApps.value = []
    appLoadFailed.value = true
    toast.error(resolveApiErrorMessage(error, t('bots.skills.loadFailed')))
  }
}

async function fetchSkillLibrary() {
  if (!props.botId) return
  const botID = props.botId
  const sequence = ++libraryLoadSequence
  isLoading.value = true
  try {
    const { data } = await getBotsByBotIdWorkspaceTargets({
      path: { bot_id: botID },
      throwOnError: true,
    })
    if (props.botId !== botID || sequence !== libraryLoadSequence) return
    const workspaceTargetId = data.targets?.find(target => target.primary)?.target_id || 'native'
    await Promise.all([
      fetchSkills(workspaceTargetId),
      fetchInstalledApps(workspaceTargetId),
    ])
  } catch (error) {
    if (props.botId !== botID || sequence !== libraryLoadSequence) return
    skills.value = []
    installedApps.value = []
    appLoadFailed.value = true
    toast.error(resolveApiErrorMessage(error, t('bots.skills.loadFailed')))
  } finally {
    if (props.botId === botID && sequence === libraryLoadSequence) isLoading.value = false
  }
}

function cleanDiscoveryRoot(value: string) {
  const trimmed = value.trim()
  if (!trimmed.startsWith('/')) {
    return trimmed
  }

  const parts = trimmed.split('/')
  const stack: string[] = []
  for (const part of parts) {
    if (!part || part === '.') continue
    if (part === '..') {
      stack.pop()
      continue
    }
    stack.push(part)
  }
  return `/${stack.join('/')}`
}

function parseDiscoveryRoots(value: string) {
  return value
    .split('\n')
    .map(item => item.trim())
    .filter(Boolean)
}

function normalizeDiscoveryRoots(values: string[]) {
  const normalized: string[] = []
  const seen = new Set<string>()

  for (const value of values) {
    const cleaned = cleanDiscoveryRoot(value)
    if (!cleaned || !cleaned.startsWith('/')) continue
    if (RESERVED_DISCOVERY_ROOTS.has(cleaned) || seen.has(cleaned)) continue
    seen.add(cleaned)
    normalized.push(cleaned)
  }

  return normalized
}

function validateDiscoveryRoots(value: string) {
  const seen = new Set<string>()
  const errors: string[] = []

  for (const item of parseDiscoveryRoots(value)) {
    const trimmed = item.trim()

    const cleaned = cleanDiscoveryRoot(trimmed)
    if (!cleaned.startsWith('/')) {
      errors.push(t('bots.skills.discoveryPathAbsolute'))
      continue
    }
    if (RESERVED_DISCOVERY_ROOTS.has(cleaned)) {
      errors.push(t('bots.skills.discoveryPathReserved'))
      continue
    }
    if (seen.has(cleaned)) {
      errors.push(t('bots.skills.discoveryPathDuplicate'))
      continue
    }

    seen.add(cleaned)
  }

  return [...new Set(errors)]
}

function areStringListsEqual(left: string[], right: string[]) {
  return left.length === right.length && left.every((item, index) => item === right[index])
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}

function readDiscoveryRoots(metadata: Record<string, unknown> | undefined) {
  const workspace = metadata?.[WORKSPACE_METADATA_KEY]
  if (!isRecord(workspace)) {
    return [...DEFAULT_DISCOVERY_ROOTS]
  }

  if (!Object.prototype.hasOwnProperty.call(workspace, SKILL_DISCOVERY_ROOTS_METADATA_KEY)) {
    return [...DEFAULT_DISCOVERY_ROOTS]
  }

  const rawRoots = workspace[SKILL_DISCOVERY_ROOTS_METADATA_KEY]
  if (!Array.isArray(rawRoots)) {
    return []
  }

  return normalizeDiscoveryRoots(
    rawRoots.filter((value): value is string => typeof value === 'string'),
  )
}

function withDiscoveryRootsMetadata(metadata: Record<string, unknown> | undefined, roots: string[]) {
  const nextMetadata = isRecord(metadata) ? { ...metadata } : {}
  const workspaceSection = isRecord(nextMetadata[WORKSPACE_METADATA_KEY])
    ? { ...(nextMetadata[WORKSPACE_METADATA_KEY] as Record<string, unknown>) }
    : {}

  workspaceSection[SKILL_DISCOVERY_ROOTS_METADATA_KEY] = normalizeDiscoveryRoots(roots)
  nextMetadata[WORKSPACE_METADATA_KEY] = workspaceSection
  return nextMetadata
}

function syncDiscoveryRoots(roots: string[]) {
  const nextRoots = [...roots]
  discoveryRootsDraft.value = nextRoots.join('\n')
  savedDiscoveryRoots.value = nextRoots
}

function resetDiscoveryRoots() {
  syncDiscoveryRoots(savedDiscoveryRoots.value)
}

function closeDiscoveryDialog() {
  resetDiscoveryRoots()
  isDiscoveryDialogOpen.value = false
}

function handleCreate() {
  isViewing.value = false
  isEditing.value = false
  editingSourcePath.value = ''
  draftRaw.value = SKILL_TEMPLATE
  isDialogOpen.value = true
}

function handleEdit(skill: SkillItem) {
  isViewing.value = false
  isEditing.value = true
  editingSourcePath.value = skill.source_path || ''
  draftRaw.value = skill.raw || ''
  isDialogOpen.value = true
}

function handleView(skill: SkillItem) {
  isViewing.value = true
  isEditing.value = false
  editingSourcePath.value = ''
  draftRaw.value = skill.raw || ''
  isDialogOpen.value = true
}

function openApp(key: string) {
  selectedAppKey.value = key
}

function closeApp() {
  selectedAppKey.value = ''
}

// Apps are removed from the Apps tab, where the removal plan
// (shared dependencies, connections) is shown before anything runs.
function openAppsTab() {
  void router.replace({ query: { ...route.query, tab: 'apps' } }).catch(() => {})
}

function skillKey(skill: SkillItem) {
  return skill.source_path || `${skill.name || 'unknown'}:${skill.source_kind || 'unknown'}`
}

function isSkillActionPending(skill: SkillItem, action: string) {
  return isActioning.value && actionTargetPath.value === skill.source_path && actionName.value === action
}

function sourceKindLabel(kind?: string) {
  switch (kind) {
    case 'legacy':
      return t('bots.skills.legacyBadge')
    case 'compat':
      return t('bots.skills.compatBadge')
    default:
      return t('bots.skills.managedBadge')
  }
}

function skillStateLabel(skill: SkillItem) {
  switch (skill.state) {
    case 'shadowed':
      return t('bots.skills.shadowedBadge')
    case 'disabled':
      return t('bots.skills.disabledBadge')
    default:
      return t('bots.skills.effectiveBadge')
  }
}

function sourceSummary(skill: SkillItem) {
  const sourcePath = skill.source_path || ''
  if (!sourcePath) return ''
  if (!skill.source_kind || skill.source_kind === 'managed' || skill.source_kind === 'registry') {
    return sourcePath
  }
  return `${sourceKindLabel(skill.source_kind)} · ${sourcePath}`
}

async function handleSkillAction(action: 'adopt' | 'disable' | 'enable', skill: SkillItem) {
  if (!skill.source_path) return
  isActioning.value = true
  actionTargetPath.value = skill.source_path
  actionName.value = action
  try {
    await postBotsByBotIdContainerSkillsActions({
      path: { bot_id: props.botId },
      body: {
        action,
        target_path: skill.source_path,
      },
      throwOnError: true,
    })
    toast.success(
      action === 'adopt'
        ? t('bots.skills.adoptSuccess')
        : action === 'disable'
          ? t('bots.skills.disableSuccess')
          : t('bots.skills.enableSuccess'),
    )
    await fetchSkills()
  } catch (error) {
    toast.error(resolveApiErrorMessage(
      error,
      action === 'adopt'
        ? t('bots.skills.adoptFailed')
        : action === 'disable'
          ? t('bots.skills.disableFailed')
          : t('bots.skills.enableFailed'),
    ))
  } finally {
    isActioning.value = false
    actionTargetPath.value = ''
    actionName.value = ''
  }
}

async function handleSave() {
  if (!canSave.value) return
  isSaving.value = true
  try {
    await postBotsByBotIdContainerSkills({
      path: { bot_id: props.botId },
      body: {
        skills: [draftRaw.value],
        ...(isEditing.value && editingSourcePath.value
          ? { source_path: editingSourcePath.value }
          : {}),
      },
      throwOnError: true,
    })
    toast.success(t('bots.skills.saveSuccess'))
    isDialogOpen.value = false
    editingSourcePath.value = ''
    await fetchSkills()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.skills.saveFailed')))
  } finally {
    isSaving.value = false
  }
}

async function handleSaveDiscoveryRoots() {
  if (!canSaveDiscoveryRoots.value) return

  isSavingDiscoveryRoots.value = true
  try {
    const metadata = withDiscoveryRootsMetadata(
      bot.value?.metadata as Record<string, unknown> | undefined,
      normalizedDiscoveryRootDrafts.value,
    )

    await putBotsById({
      path: { id: props.botId },
      body: { metadata },
      throwOnError: true,
    })

    void queryCache.invalidateQueries({ key: ['bot', props.botId] })
    void queryCache.invalidateQueries({ key: ['bot'] })
    void queryCache.invalidateQueries({ key: getBotsQueryKey() })

    syncDiscoveryRoots(normalizedDiscoveryRootDrafts.value)
    isDiscoveryDialogOpen.value = false
    toast.success(t('bots.skills.discoverySaveSuccess'))

    await Promise.all([
      refetchBot(),
      fetchSkills(),
    ])
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.skills.discoverySaveFailed')))
  } finally {
    isSavingDiscoveryRoots.value = false
  }
}

async function handleDelete(skill: SkillItem) {
  // Deleting by source_path keeps registry skills (nested under their registry
  // and app) distinct from a flat managed skill that shares the short name.
  const sourcePath = skill.source_path
  if (!sourcePath) return
  isDeleting.value = true
  deletingPath.value = sourcePath
  try {
    await deleteBotsByBotIdContainerSkills({
      path: { bot_id: props.botId },
      body: {
        source_paths: [sourcePath],
      },
      throwOnError: true,
    })
    toast.success(t('bots.skills.deleteSuccess'))
    await fetchSkills()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.skills.deleteFailed')))
  } finally {
    isDeleting.value = false
    deletingPath.value = ''
  }
}

watch(() => props.botId, () => {
  if (!props.botId) return
  isDiscoveryDialogOpen.value = false
  selectedAppKey.value = ''
  skills.value = []
  installedApps.value = []
  appLoadFailed.value = false
  syncDiscoveryRoots(DEFAULT_DISCOVERY_ROOTS)
  void fetchSkillLibrary()
}, { immediate: true })

let hasActivated = false
onActivated(() => {
  if (!hasActivated) {
    hasActivated = true
    return
  }
  if (props.botId) void fetchSkillLibrary()
})

watch(bot, (value) => {
  if (!value) return
  if (isDiscoveryRootsDirty.value && !isSavingDiscoveryRoots.value) return
  syncDiscoveryRoots(readDiscoveryRoots(value.metadata as Record<string, unknown> | undefined))
}, { immediate: true })

watch(isDiscoveryDialogOpen, (open, prevOpen) => {
  if (!open && prevOpen && !isSavingDiscoveryRoots.value && (isDiscoveryDraftModified.value || hasDiscoveryRootErrors.value)) {
    resetDiscoveryRoots()
  }
})
</script>
