<template>
  <div
    data-codex-project-bar
    class="mx-3 -mb-3 flex min-w-0 items-center gap-1 rounded-t-2xl bg-muted px-2 pt-1 pb-4 font-normal text-muted-foreground"
  >
    <!-- One shared hover surface; the two native buttons keep selecting and
         clearing separate for keyboard users without nesting buttons. -->
    <Button
      v-if="editable"
      as="div"
      role="group"
      variant="ghost"
      size="sm"
      shape="circle"
      block
      class="group/project min-w-0 w-auto shrink gap-0 p-0"
      :class="[project ? 'pl-1' : undefined, locked ? 'pointer-events-none' : undefined]"
      :data-state="projectMenuOpen ? 'open' : undefined"
      :aria-label="t('chat.codexProject.choose')"
    >
      <Button
        v-if="project"
        variant="quiet"
        size="icon-sm"
        class="w-7 text-foreground"
        :disabled="locked"
        :title="t('chat.codexProject.clear')"
        :aria-label="t('chat.codexProject.clear')"
        @click="emit('clear')"
      >
        <Folder class="group-hover/project:hidden group-focus-within/project:hidden" />
        <CircleX class="hidden group-hover/project:block group-focus-within/project:block" />
      </Button>
      <DropdownMenu v-model:open="projectMenuOpen">
        <DropdownMenuTrigger as-child>
          <Button
            variant="quiet"
            size="sm"
            class="min-w-0 shrink px-2.5 text-label font-normal text-foreground"
            :class="project ? 'pl-0' : undefined"
            :disabled="locked"
            :title="project?.path"
            :aria-label="t('chat.codexProject.choose')"
          >
            <Folder v-if="!project" />
            <span class="truncate">{{ project?.name || t('chat.codexProject.choose') }}</span>
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent
          align="start"
          side="top"
          class="w-72 max-w-[calc(100vw-2rem)]"
        >
          <DropdownMenuLabel>{{ t('chat.codexProject.choose') }}</DropdownMenuLabel>
          <DropdownMenuItem
            v-if="!projects.length"
            disabled
            class="whitespace-normal"
          >
            {{ t('chat.codexProject.empty') }}
          </DropdownMenuItem>
          <DropdownMenuItem
            v-for="folder in projects"
            :key="folder.id"
            :disabled="locked"
            @select="emit('select', folder)"
          >
            <Folder />
            <span class="min-w-0 flex-1">
              <span class="block truncate">{{ folder.name }}</span>
              <span class="block truncate text-caption text-muted-foreground">{{ folder.path }}</span>
            </span>
            <Check v-if="folder.id === project?.id" />
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </Button>
    <span
      v-else
      class="inline-flex h-8 min-w-0 items-center gap-1.5 px-2.5 text-label"
      :title="project?.path"
    >
      <Folder class="size-4 shrink-0" />
      <span
        class="truncate"
        :class="project ? 'text-composer-control-label' : undefined"
      >{{ project?.name || t('chat.codexProject.none') }}</span>
    </span>
    <template v-if="project">
      <span class="inline-flex h-8 min-w-0 shrink-0 items-center gap-1.5 px-2.5 text-label">
        <Cloud class="size-4 shrink-0" />
        <span>{{ t('chat.codexProject.workspace') }}</span>
      </span>
      <DropdownMenu
        v-if="branchLabel && canSwitchBranch"
        v-model:open="branchMenuOpen"
      >
        <DropdownMenuTrigger as-child>
          <Button
            variant="ghost"
            size="sm"
            class="min-w-0 text-label font-normal"
            :disabled="locked || switchingBranch"
            :loading="switchingBranch"
            :title="t('chat.codexProject.chooseBranch')"
            :aria-label="t('chat.codexProject.chooseBranch')"
          >
            <GitBranch />
            <span class="truncate">{{ branchLabel }}</span>
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent
          align="start"
          side="top"
          class="w-64 max-w-[calc(100vw-2rem)]"
        >
          <DropdownMenuLabel>{{ t('chat.codexProject.chooseBranch') }}</DropdownMenuLabel>
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
            {{ t('chat.codexProject.noBranches') }}
          </DropdownMenuItem>
          <DropdownMenuItem
            v-for="name in branchState?.branches ?? []"
            :key="name"
            :disabled="locked || switchingBranch || branchState?.busy || name === branchState?.branch"
            @select="switchBranch(name)"
          >
            <GitBranch />
            <span class="min-w-0 flex-1 truncate">{{ name }}</span>
            <Check v-if="name === branchState?.branch" />
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <span
        v-else-if="branchLabel"
        class="inline-flex h-8 min-w-0 items-center gap-1.5 px-2.5 text-label"
        :title="branchLabel"
      >
        <GitBranch class="size-4 shrink-0" />
        <span class="truncate">{{ branchLabel }}</span>
      </span>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQuery, useQueryCache } from '@pinia/colada'
import { useDocumentVisibility, useIntervalFn } from '@vueuse/core'
import { Check, CircleX, Cloud, Folder, GitBranch } from 'lucide-vue-next'
import {
  Button, DropdownMenu, DropdownMenuContent, DropdownMenuItem,
  DropdownMenuLabel, DropdownMenuTrigger, toast,
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
}>()
const emit = defineEmits<{ select: [project: BotWorkdir]; clear: [] }>()
const { t } = useI18n()
const projectMenuOpen = ref(false)
const branchMenuOpen = ref(false)
const switchingBranch = ref(false)
const queryCache = useQueryCache()
const visibility = useDocumentVisibility()
const branchQueryEnabled = () => props.visible && visibility.value === 'visible' && !!props.botId && !!props.project?.id
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
const branchLabel = computed(() => branchState.value?.branch || (branchState.value?.branches?.length ? t('chat.codexProject.detachedHead') : ''))
const canSwitchBranch = computed(() => props.canExecute && props.project?.target_kind === 'native' && !props.project.archived)
useIntervalFn(() => { if (branchQueryEnabled()) void branchQuery.refetch() }, 5000)
watch(branchMenuOpen, (open) => { if (open) void branchQuery.refetch() })
watch(() => props.project?.id, () => { branchMenuOpen.value = false })

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
  if (wasStreaming && !streaming && props.visible && props.project?.id) void branchQuery.refetch()
})
</script>
