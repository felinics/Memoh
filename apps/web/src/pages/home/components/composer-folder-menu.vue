<template>
  <!-- Folder session-attribute trigger: a bare text trigger under the composer.
       Drafts may pick a folder; once the session exists the binding is pinned
       and the label goes read-only. Direct runtimes can still use the menu
       to inspect and switch the project branch. -->
  <DropdownMenu v-model:open="menuOpen">
    <DropdownMenuTrigger as-child>
      <Button
        variant="quiet"
        size="sm"
        :disabled="locked"
        class="min-w-14 shrink max-w-48 gap-1.5 px-1.5 font-normal max-md:h-11"
        :title="project?.path || folderName"
        :aria-label="t('chat.folder') + ': ' + (project?.name || folderName || t('chat.runtimeProject.none'))"
      >
        <Folder class="size-3.5 shrink-0" />
        <span class="truncate text-label">{{ triggerLabel }}</span>
        <ChevronDown
          v-if="menuEnabled"
          class="size-3 shrink-0 opacity-70"
        />
      </Button>
    </DropdownMenuTrigger>
    <DropdownMenuContent
      v-if="menuEnabled"
      align="start"
      side="bottom"
      :side-offset="0"
      class="w-72 max-w-[calc(100vw-2rem)]"
    >
      <DropdownMenuLabel>{{ t('chat.folder') }}</DropdownMenuLabel>
      <DropdownMenuItem
        v-if="pickable"
        @select="emit('clear')"
      >
        <X class="size-4 shrink-0" />
        <span class="min-w-0 flex-1 truncate">{{ t(gitBranches ? 'chat.runtimeProject.clear' : 'chat.folderDetachDraft') }}</span>
        <Check
          v-if="!project"
          class="ml-auto"
        />
      </DropdownMenuItem>
      <DropdownMenuItem
        v-if="pickable && !projects.length"
        disabled
        class="whitespace-normal"
      >
        {{ t('chat.runtimeProject.empty') }}
      </DropdownMenuItem>
      <DropdownMenuItem
        v-for="folder in projects"
        :key="folder.id"
        :disabled="locked || !editable"
        @select="emit('select', folder)"
      >
        <Folder />
        <span class="min-w-0 flex-1">
          <span class="block truncate">{{ folder.name }}</span>
          <span
            v-if="gitBranches"
            class="block truncate text-caption text-muted-foreground"
          >{{ folder.path }}</span>
        </span>
        <Check v-if="folder.id === project?.id" />
      </DropdownMenuItem>
      <DropdownMenuItem
        v-if="!pickable && project"
        disabled
      >
        <Folder />
        <span class="min-w-0 flex-1">
          <span class="block truncate">{{ project.name }}</span>
          <span
            v-if="project.path"
            class="block truncate text-caption text-muted-foreground"
          >{{ project.path }}</span>
        </span>
        <Check class="ml-auto" />
      </DropdownMenuItem>
      <DropdownMenuItem
        v-if="!pickable && !project"
        disabled
      >
        <Folder />
        <span class="min-w-0 flex-1 truncate">{{ folderName }}</span>
      </DropdownMenuItem>

      <template v-if="gitBranches && project">
        <DropdownMenuSeparator />
        <DropdownMenuLabel>{{ t('chat.runtimeProject.chooseBranch') }}</DropdownMenuLabel>
        <DropdownMenuItem
          v-if="branchState?.busy"
          disabled
          class="whitespace-normal"
        >
          {{ t('errors.workdir.git_busy') }}
        </DropdownMenuItem>
        <DropdownMenuItem
          v-if="!branchState?.branches?.length"
          disabled
          class="whitespace-normal"
        >
          {{ t('chat.runtimeProject.noBranches') }}
        </DropdownMenuItem>
        <DropdownMenuItem
          v-for="name in branchState?.branches ?? []"
          :key="name"
          :disabled="locked || switchingBranch || branchState?.busy || !canSwitchBranch || name === branchState?.branch"
          @select="switchBranch(name)"
        >
          <GitBranch />
          <span class="min-w-0 flex-1 truncate">{{ name }}</span>
          <Check v-if="name === branchState?.branch" />
        </DropdownMenuItem>
      </template>
    </DropdownMenuContent>
  </DropdownMenu>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQuery, useQueryCache } from '@pinia/colada'
import { useDocumentVisibility, useIntervalFn } from '@vueuse/core'
import { Check, ChevronDown, Folder, GitBranch, X } from 'lucide-vue-next'
import {
  Button, DropdownMenu, DropdownMenuContent, DropdownMenuItem,
  DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger, toast,
} from '@felinic/ui'
import { getBotsByBotIdWorkdirsByWorkdirIdGitBranch, postBotsByBotIdWorkdirsByWorkdirIdGitBranch } from '@memohai/sdk'
import type { BotWorkdir } from '@/composables/api/useWorkdirs'
import { resolveApiErrorMessage } from '@/utils/api-error'

const props = defineProps<{
  botId: string
  project: BotWorkdir | null
  projects: BotWorkdir[]
  editable: boolean
  locked: boolean
  visible: boolean
  streaming: boolean
  canExecute: boolean
  gitBranches: boolean
  pickable: boolean
  lockedFolder: boolean
  folderName: string
}>()
const emit = defineEmits<{ select: [project: BotWorkdir]; clear: [] }>()
const { t } = useI18n()
const menuOpen = ref(false)
const switchingBranch = ref(false)
const queryCache = useQueryCache()
const visibility = useDocumentVisibility()

// The trigger only opens a menu when there's something to pick or show: a
// draft with folders, a locked binding worth reading, or a project
// whose branch can be switched. A draft with no folders and no binding reads
// as a plain label instead of an empty menu.
const menuEnabled = computed(() => (
  props.pickable
  || !!props.project
  || props.lockedFolder
))

const triggerLabel = computed(() => (
  props.project?.name?.trim()
  || (props.lockedFolder ? props.folderName : '')
  || t('chat.folder')
))

const branchQueryEnabled = () => props.gitBranches && props.visible && visibility.value === 'visible' && !!props.botId && !!props.project?.id
const branchQuery = useQuery({
  key: () => ['workdir-git-branch', props.botId, props.project?.id ?? ''],
  enabled: branchQueryEnabled,
  query: async ({ signal }) => {
    const workdirId = props.project!.id!
    const { data } = await getBotsByBotIdWorkdirsByWorkdirIdGitBranch({
      path: { bot_id: props.botId, workdir_id: workdirId }, signal, throwOnError: true,
    })
    return { workdirId, state: data }
  },
})
const branchState = computed(() => !branchQuery.error.value && branchQuery.data.value?.workdirId === props.project?.id
  ? branchQuery.data.value?.state : undefined)
const canSwitchBranch = computed(() => props.canExecute && props.project?.target_kind === 'native' && !props.project.archived)
useIntervalFn(() => { if (branchQueryEnabled()) void branchQuery.refetch() }, 5000)
watch(menuOpen, (open) => { if (open && branchQueryEnabled()) void branchQuery.refetch() })
watch(() => props.project?.id, () => { menuOpen.value = false })

async function switchBranch(branch: string) {
  if (!canSwitchBranch.value || props.locked || switchingBranch.value || branchState.value?.busy || !props.project?.id) return
  const botId = props.botId
  const workdirId = props.project.id
  switchingBranch.value = true
  try {
    await postBotsByBotIdWorkdirsByWorkdirIdGitBranch({
      path: { bot_id: botId, workdir_id: workdirId }, body: { branch }, throwOnError: true,
    })
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('errors.workdir.git_switch_failed')))
  } finally {
    switchingBranch.value = false
    // Other folders can refer to subdirectories of the same Git worktree.
    await queryCache.invalidateQueries({ key: ['workdir-git-branch', botId] })
  }
}
watch(() => props.streaming, (streaming, wasStreaming) => {
  if (wasStreaming && !streaming && branchQueryEnabled()) void branchQuery.refetch()
})
</script>
