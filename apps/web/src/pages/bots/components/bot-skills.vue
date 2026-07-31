<template>
  <PageShell
    variant="tab"
    :title="$t('bots.skills.title')"
    :description="$t('bots.skills.intro')"
  >
    <template #actions>
      <Button
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
        size="sm"
        @click="handleCreate"
      >
        <Plus class="size-4" />
        {{ $t('bots.skills.addSkill') }}
      </Button>
    </template>

    <SettingsSection :title="$t('bots.skills.libraryTitle')">
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
        v-else-if="!skills.length"
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

      <!-- v-else keeps the row list mutually exclusive with the loading/empty
           branches, so a refetch over existing data shows the spinner alone (the
           original behavior) rather than stale rows beneath it. -->
      <template v-else>
        <!-- Dense object-list row: a skill's name+badges, description, source path,
             and its per-skill action cluster. align="start" top-pins the action
             buttons to the title line since the description can wrap to two lines
             and the shadowed hint can add a third. -->
        <SettingsRow
          v-for="skill in skills"
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
              v-if="skill.managed"
              :message="$t('bots.skills.deleteConfirm')"
              :cancel-text="$t('common.cancel')"
              :confirm-text="$t('common.confirm')"
              :loading="isDeleting && deletingName === skill.name"
              @confirm="handleDelete(skill.name)"
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
    </SettingsSection>

    <!-- Edit Dialog (Modal IDE) -->
    <Dialog v-model:open="isDialogOpen">
      <DialogContent class="flex max-h-[calc(100vh-2rem)] flex-col overflow-hidden p-0 sm:h-[85vh] sm:max-w-4xl">
        <DialogHeader class="shrink-0 border-b border-border p-4">
          <DialogTitle class="text-sm font-semibold">
            {{ isEditing ? $t('common.edit') : $t('bots.skills.addSkill') }}
          </DialogTitle>
        </DialogHeader>
        
        <div class="min-h-0 flex-1 p-4">
          <div class="flex h-full min-h-0 flex-col overflow-hidden rounded-[var(--radius-menu-shell)] border border-border">
            <MonacoEditor
              v-model="draftRaw"
              language="markdown"
              :readonly="isSaving"
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
              {{ $t('common.cancel') }}
            </Button>
          </DialogClose>
          <Button
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
import { ArrowDownToLine, Eye, EyeOff, Plus, SlidersHorizontal, SquarePen, Trash2 } from 'lucide-vue-next'
import { computed, ref, watch } from 'vue'
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
  postBotsByBotIdContainerSkills,
  postBotsByBotIdContainerSkillsActions,
  deleteBotsByBotIdContainerSkills,
  putBotsById,
  type HandlersSkillItem,
} from '@memohai/sdk'
import { getBotsQueryKey } from '@memohai/sdk/colada'
import { resolveApiErrorMessage } from '@/utils/api-error'

type SkillItem = HandlersSkillItem & {
  source_path?: string
  source_root?: string
  source_kind?: string
  managed?: boolean
  state?: string
  shadowed_by?: string
}

const props = defineProps<{
  botId: string
}>()

const { t } = useI18n()
const queryCache = useQueryCache()

function invalidateSidebarSkills() {
  queryCache.invalidateQueries({ key: ['bot-skills-catalog', props.botId] })
}

const MANAGED_SKILL_PATH = '/data/skills'
const DEFAULT_DISCOVERY_ROOTS = ['/data/.agents/skills', '/root/.agents/skills']
const RESERVED_DISCOVERY_ROOTS = new Set(['/data/skills', '/data/.skills'])
const WORKSPACE_METADATA_KEY = 'workspace'
const SKILL_DISCOVERY_ROOTS_METADATA_KEY = 'skill_discovery_roots'

const isLoading = ref(false)
const isSaving = ref(false)
const isDeleting = ref(false)
const deletingName = ref('')
const isActioning = ref(false)
const actionTargetPath = ref('')
const actionName = ref('')
const skills = ref<SkillItem[]>([])
const isSavingDiscoveryRoots = ref(false)
const isDiscoveryDialogOpen = ref(false)
const discoveryRootsDraft = ref(DEFAULT_DISCOVERY_ROOTS.join('\n'))
const savedDiscoveryRoots = ref<string[]>([...DEFAULT_DISCOVERY_ROOTS])

const isDialogOpen = ref(false)
const isEditing = ref(false)
const draftRaw = ref('')

const SKILL_TEMPLATE = `---
name: my-skill
description: Brief description
---

# My Skill
`

const canSave = computed(() => {
  return draftRaw.value.trim().length > 0
})

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

async function fetchSkills() {
  if (!props.botId) return
  isLoading.value = true
  try {
    const { data } = await getBotsByBotIdContainerSkills({
      path: { bot_id: props.botId },
      throwOnError: true,
    })
    skills.value = data.skills || []
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.skills.loadFailed')))
  } finally {
    isLoading.value = false
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
  isEditing.value = false
  draftRaw.value = SKILL_TEMPLATE
  isDialogOpen.value = true
}

function handleEdit(skill: HandlersSkillItem) {
  isEditing.value = true
  draftRaw.value = skill.raw || ''
  isDialogOpen.value = true
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
    case 'plugin':
      return t('bots.skills.pluginBadge')
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
  if (!skill.source_kind || skill.source_kind === 'managed') {
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
    invalidateSidebarSkills()
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
      },
      throwOnError: true,
    })
    toast.success(t('bots.skills.saveSuccess'))
    isDialogOpen.value = false
    await fetchSkills()
    invalidateSidebarSkills()
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

async function handleDelete(name?: string) {
  if (!name) return
  isDeleting.value = true
  deletingName.value = name
  try {
    await deleteBotsByBotIdContainerSkills({
      path: { bot_id: props.botId },
      body: {
        names: [name],
      },
      throwOnError: true,
    })
    toast.success(t('bots.skills.deleteSuccess'))
    await fetchSkills()
    invalidateSidebarSkills()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.skills.deleteFailed')))
  } finally {
    isDeleting.value = false
    deletingName.value = ''
  }
}

watch(() => props.botId, () => {
  if (!props.botId) return
  isDiscoveryDialogOpen.value = false
  syncDiscoveryRoots(DEFAULT_DISCOVERY_ROOTS)
  void fetchSkills()
}, { immediate: true })

// Refresh local skills list when chat-sidebar invalidates the shared catalog cache.
watch(
  () => {
    const entries = queryCache.getEntries({ key: ['bot-skills-catalog', props.botId] })
    return entries[0]?.state.value.data
  },
  (next, prev) => {
    if (!props.botId) return
    if (next === prev) return
    void fetchSkills()
  },
)

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
