<template>
  <FormDialogShell
    v-model:open="open"
    :title="t('bots.projects.createTitle')"
    :description="t('bots.projects.createDescription')"
    :cancel-text="t('common.cancel')"
    :submit-text="t('bots.projects.create')"
    :submit-disabled="!canSubmit"
    :loading="creating"
    @submit="handleCreate"
  >
    <template #body>
      <FormStack class="mt-4">
        <FieldStack
          :label="t('bots.projects.form.name')"
          for="project-name"
        >
          <Input
            id="project-name"
            v-model="name"
            :placeholder="t('bots.projects.form.namePlaceholder')"
          />
        </FieldStack>
        <FieldStack
          v-if="selectableTargets.length > 1"
          :label="t('bots.projects.form.target')"
        >
          <Select v-model="targetId">
            <SelectTrigger class="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem
                v-for="target in selectableTargets"
                :key="target.target_id"
                :value="target.target_id"
              >
                {{ targetDisplayName(target) }}
              </SelectItem>
            </SelectContent>
          </Select>
        </FieldStack>
        <!-- Native workspace: browse and pick — a mistyped path is this
             feature's main failure mode, so the directory is chosen, not
             typed. Remote computers cannot be browsed yet; the path is typed
             and the backend verifies the directory exists. -->
        <FieldStack
          v-if="targetIsNative"
          :label="t('bots.projects.form.directory')"
          :help="t('bots.projects.form.directoryHelp', { path: browsePath })"
        >
          <div class="overflow-hidden rounded-md border border-border">
            <div class="flex items-center gap-1.5 border-b border-border px-2 py-1.5">
              <TextButton
                :aria-label="t('bots.projects.form.directoryUp')"
                :disabled="browseAtRoot || browseLoading"
                @click="browseUp"
              >
                <CornerLeftUp />
              </TextButton>
              <span class="min-w-0 flex-1 truncate font-mono text-body text-muted-foreground">{{ browsePath }}</span>
            </div>
            <Command>
              <CommandList class="max-h-44">
                <InlineLoadingRow
                  v-if="browseLoading"
                  size="sm"
                  class="px-3 py-2"
                />
                <template v-else>
                  <CommandItem
                    v-for="dir in browseDirs"
                    :key="dir"
                    :value="dir"
                    @select="browseInto(dir)"
                  >
                    <Folder />
                    <span class="min-w-0 flex-1 truncate">{{ dir }}</span>
                  </CommandItem>
                  <p
                    v-if="browseDirs.length === 0"
                    class="px-3 py-4 text-center text-body text-muted-foreground"
                  >
                    {{ t('bots.projects.form.noSubdirectories') }}
                  </p>
                </template>
              </CommandList>
            </Command>
          </div>
        </FieldStack>
        <FieldStack
          v-else
          :label="t('bots.projects.form.path')"
          for="project-path"
          :help="t('bots.projects.form.remotePathHelp')"
        >
          <Input
            id="project-path"
            v-model="remotePath"
            :placeholder="t('bots.projects.form.remotePathPlaceholder')"
          />
        </FieldStack>
      </FormStack>
    </template>
  </FormDialogShell>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQuery } from '@pinia/colada'
import { CornerLeftUp, Folder } from 'lucide-vue-next'
import {
  Command,
  CommandItem,
  CommandList,
  FieldStack,
  FormDialogShell,
  FormStack,
  InlineLoadingRow,
  Input,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  TextButton,
  toast,
} from '@felinic/ui'
import { getBotsByBotIdContainerFsList, getBotsByBotIdWorkspaceTargets, type WorkspaceWorkspaceTarget } from '@memohai/sdk'
import { createProject } from '@/composables/api/useProjects'
import { useProjectsStore } from '@/store/projects'
import { resolveApiErrorMessage } from '@/utils/api-error'

const props = defineProps<{ botId: string }>()
const open = defineModel<boolean>('open', { default: false })
const emit = defineEmits<{ created: [] }>()

const { t } = useI18n()
const projectsStore = useProjectsStore()

// Workspace targets back the location choice; the select only appears when a
// remote computer is actually bound.
const { data: targetsResponse } = useQuery({
  key: () => ['bot-workspace-targets', props.botId],
  query: async () => {
    const { data } = await getBotsByBotIdWorkspaceTargets({
      path: { bot_id: props.botId },
      throwOnError: true,
    })
    return data
  },
  enabled: () => !!props.botId && open.value,
})
const selectableTargets = computed<WorkspaceWorkspaceTarget[]>(() => (
  (targetsResponse.value?.targets ?? []).filter(target => (target.target_id ?? '').trim())
))

function targetDisplayName(target: WorkspaceWorkspaceTarget): string {
  const name = (target.name ?? '').trim()
  if (name) return name
  return target.kind === 'remote' ? t('bots.projects.targetRemote') : t('bots.projects.targetNative')
}

const creating = ref(false)
const name = ref('')
const targetId = ref('native')
const remotePath = ref('')

const targetIsNative = computed(() => {
  const target = selectableTargets.value.find(item => item.target_id === targetId.value)
  return !target || target.kind !== 'remote'
})

watch(open, (isOpen) => {
  if (!isOpen) return
  name.value = ''
  targetId.value = 'native'
  remotePath.value = ''
  browsePath.value = WORKSPACE_ROOT
  void loadBrowseDirs()
})

const canSubmit = computed(() => {
  if (!name.value.trim()) return false
  if (targetIsNative.value) return !browseLoading.value
  return !!remotePath.value.trim()
})

async function handleCreate() {
  if (!canSubmit.value || creating.value) return
  creating.value = true
  try {
    await createProject(props.botId, {
      name: name.value.trim(),
      path: targetIsNative.value ? browsePath.value : remotePath.value.trim(),
      workspaceTargetId: targetId.value,
    })
    await projectsStore.refreshProjects(props.botId)
    open.value = false
    toast.success(t('bots.projects.created'))
    emit('created')
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.projects.createFailed')))
  } finally {
    creating.value = false
  }
}

// ---- native directory browser ----
const WORKSPACE_ROOT = '/data'
const browsePath = ref(WORKSPACE_ROOT)
const browseDirs = ref<string[]>([])
const browseLoading = ref(false)
const browseAtRoot = computed(() => browsePath.value === WORKSPACE_ROOT)
let browseRequest = 0

async function loadBrowseDirs() {
  const request = ++browseRequest
  browseLoading.value = true
  try {
    const { data } = await getBotsByBotIdContainerFsList({
      path: { bot_id: props.botId },
      query: { path: browsePath.value },
      throwOnError: true,
    })
    if (request !== browseRequest) return
    browseDirs.value = (data.entries ?? [])
      .filter(entry => entry.isDir && (entry.name ?? '').trim() && !(entry.name ?? '').startsWith('.'))
      .map(entry => entry.name ?? '')
      .sort((a, b) => a.localeCompare(b))
  } catch (error) {
    if (request !== browseRequest) return
    browseDirs.value = []
    toast.error(resolveApiErrorMessage(error, t('bots.projects.form.browseFailed')))
  } finally {
    if (request === browseRequest) browseLoading.value = false
  }
}

function browseInto(dir: string) {
  browsePath.value = `${browsePath.value.replace(/\/$/, '')}/${dir}`
  void loadBrowseDirs()
}

function browseUp() {
  if (browseAtRoot.value) return
  const parent = browsePath.value.slice(0, browsePath.value.lastIndexOf('/'))
  browsePath.value = parent.length < WORKSPACE_ROOT.length ? WORKSPACE_ROOT : parent
  void loadBrowseDirs()
}
</script>
