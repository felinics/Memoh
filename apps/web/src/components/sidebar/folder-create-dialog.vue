<template>
  <FormDialogShell
    v-model:open="open"
    :title="t('bots.folders.createTitle')"
    :description="t('bots.folders.createDescription')"
    :cancel-text="t('common.cancel')"
    :submit-text="t('bots.folders.create')"
    :submit-disabled="!canSubmit"
    :loading="creating"
    @submit="handleCreate"
  >
    <template #body>
      <FormStack class="mt-4">
        <FieldStack
          :label="t('bots.folders.form.name')"
          for="folder-name"
        >
          <Input
            id="folder-name"
            v-model="name"
            :placeholder="t('bots.folders.form.namePlaceholder')"
          />
        </FieldStack>
        <FieldStack
          v-if="selectableTargets.length > 1"
          :label="t('bots.folders.form.target')"
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
        <!-- Native workspace: the folder's name IS its directory, and the tree
             highlights that directory — the row you see selected and the name
             you see typed are one thing, in both directions. Picking an
             existing directory is therefore a single click (it fills the name),
             and the directory is created when it doesn't exist yet. The tree is
             the Explorer's, not a menu: choosing where a folder lives is the
             same act as reading the workspace, so it reads the same (see
             file-manager/tree-row). A connected computer has no name/directory
             coupling: the path is the folder's, typed or picked from the same
             tree, and it must already exist. -->
        <FieldStack
          v-if="targetIsNative"
          :label="t('bots.folders.form.parent')"
          :help="nativePathHelp"
        >
          <DirectoryPicker
            :bot-id="botId"
            :root-label="selectedTargetName"
            :selected-path="nativeSelectedPath"
            @select="selectDirectory"
          />
        </FieldStack>
        <FieldStack
          v-else
          :label="t('bots.folders.form.path')"
          for="folder-path"
          :help="remotePathHelp"
        >
          <div class="flex flex-col gap-2">
            <Input
              id="folder-path"
              v-model="remotePath"
              :placeholder="t('bots.folders.form.remotePathPlaceholder')"
            />
            <!-- Keyed by target: switching computers rebuilds the tree, so a
                 listing still in flight for the previous one has nowhere to
                 land. Rooted at the computer's workspace base (its home);
                 anything outside it is still one typed path away. -->
            <DirectoryPicker
              v-if="selectedTargetOnline"
              :key="targetId"
              :bot-id="botId"
              :target-id="targetId"
              :root-label="selectedTargetName"
              :selected-path="remotePath.trim()"
              @select="remotePath = $event"
            />
          </div>
        </FieldStack>
      </FormStack>
    </template>
  </FormDialogShell>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQuery } from '@pinia/colada'
import {
  FieldStack,
  FormDialogShell,
  FormStack,
  Input,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  toast,
} from '@felinic/ui'
import { getBotsByBotIdWorkspaceTargets, postBotsByBotIdContainerFsMkdir, type WorkspaceWorkspaceTarget } from '@memohai/sdk'
import DirectoryPicker from '@/components/file-manager/directory-picker.vue'
import { parentPath } from '@/components/file-manager/utils'
import { createWorkdir } from '@/composables/api/useWorkdirs'
import { useWorkdirsStore } from '@/store/workdirs'
import { resolveApiErrorMessage } from '@/utils/api-error'

const props = defineProps<{ botId: string }>()
const open = defineModel<boolean>('open', { default: false })
const emit = defineEmits<{ created: [] }>()

const { t } = useI18n()
const workdirsStore = useWorkdirsStore()

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
  return target.kind === 'remote' ? t('bots.folders.targetRemote') : t('bots.folders.targetNative')
}

const creating = ref(false)
const name = ref('')
const targetId = ref('native')
const remotePath = ref('')

const selectedTarget = computed(() => (
  selectableTargets.value.find(item => item.target_id === targetId.value)
))

const targetIsNative = computed(() => selectedTarget.value?.kind !== 'remote')

// A computer that is not connected can neither be browsed nor validate a path,
// so the tree gives way to a line saying so and Create waits for it.
const selectedTargetOnline = computed(() => selectedTarget.value?.online !== false)

const remotePathHelp = computed(() => (
  selectedTargetOnline.value
    ? t('bots.folders.form.remotePathHelp')
    : t('bots.folders.form.remoteOffline', { name: selectedTargetName.value })
))

// On the native workspace the name doubles as the directory segment, so it has
// to be one: a name carrying a separator (or a dot segment) would silently land
// somewhere other than where the help line promises.
const nameIsDirectorySegment = computed(() => {
  const value = name.value.trim()
  if (!value || value === '.' || value === '..') return false
  return !/[/\\]/.test(value)
})

const nativeTargetPath = computed(() => (
  `${browsePath.value.replace(/\/$/, '')}/${name.value.trim()}`
))

// The help line always states the resolved absolute path — the tree shows
// structure, this shows the exact directory the sessions get. It also says the
// directory is created when missing, so nothing appears out of nowhere and the
// line stays true whether or not it already exists.
const nativePathHelp = computed(() => (
  nameIsDirectorySegment.value
    ? t('bots.folders.form.directoryTarget', { path: nativeTargetPath.value })
    : t('bots.folders.form.parentHelp', { path: browsePath.value })
))

// The root row is the surface itself — the Cloud Computer rather than a
// directory named "data", a connected computer rather than its home path.
const selectedTargetName = computed(() => (
  selectedTarget.value ? targetDisplayName(selectedTarget.value) : t('bots.folders.targetNative')
))

const canSubmit = computed(() => {
  if (!name.value.trim()) return false
  if (targetIsNative.value) return nameIsDirectorySegment.value
  return selectedTargetOnline.value && !!remotePath.value.trim()
})

async function handleCreate() {
  if (!canSubmit.value || creating.value) return
  creating.value = true
  try {
    const path = targetIsNative.value ? nativeTargetPath.value : remotePath.value.trim()
    // Mkdir is MkdirAll server-side, so an existing directory is a no-op and
    // the workdir binds to it unchanged.
    if (targetIsNative.value) {
      await postBotsByBotIdContainerFsMkdir({
        path: { bot_id: props.botId },
        body: { path },
        throwOnError: true,
      })
    }
    await createWorkdir(props.botId, {
      name: name.value.trim(),
      path,
      workspaceTargetId: targetId.value,
    })
    await workdirsStore.refreshWorkdirs(props.botId)
    open.value = false
    toast.success(t('bots.folders.created'))
    emit('created')
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.folders.createFailed')))
  } finally {
    creating.value = false
  }
}

// The directory the new folder is created IN; the name field is the folder's
// own segment, so `browsePath/name` is the target. DirectoryPicker owns the
// listing, lazy expansion and its own failure/retry — this is just the state.
const WORKSPACE_ROOT = '/data'
const browsePath = ref(WORKSPACE_ROOT)

// The tree highlights the TARGET, not the parent, so the row you see selected
// and the name you see typed are always the same directory — clicking `df`
// fills the name with "df", and typing a name that already exists lights up its
// row. With no name yet there is no target, so the parent stays lit.
const nativeSelectedPath = computed(() => (
  nameIsDirectorySegment.value ? nativeTargetPath.value : browsePath.value
))

// Clicking a directory makes it the target: it supplies the name, and its
// parent becomes the create-in location. The root row is the one exception —
// it is a location only (a folder needs a name of its own), so it clears the
// name rather than targeting the workspace root, which is the binding that
// made every folder resolve to /data in the first place.
function selectDirectory(path: string) {
  if (path === WORKSPACE_ROOT) {
    browsePath.value = WORKSPACE_ROOT
    name.value = ''
    return
  }
  browsePath.value = parentPath(path)
  name.value = path.slice(path.lastIndexOf('/') + 1)
}

// A path belongs to one computer; carrying it to another would quietly point
// the folder at whatever happens to live at the same path there.
watch(targetId, () => {
  remotePath.value = ''
})

// Every open starts from a clean form. The picker is unmounted while the dialog
// is closed, so it re-roots itself from this reset on the next open.
watch(open, (isOpen) => {
  if (!isOpen) return
  name.value = ''
  targetId.value = 'native'
  remotePath.value = ''
  browsePath.value = WORKSPACE_ROOT
}, { immediate: true })
</script>
