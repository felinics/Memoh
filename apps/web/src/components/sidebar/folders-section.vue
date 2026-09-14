<template>
  <!-- Folders — a sidebar section that is a SIBLING of Recents, never a
       group inside it: folders organize workdir-bound chats, Recents keeps
       the ungrouped timeline. This section does NOT scroll and has no height
       cap: it flows in the sessions panel's single scroll container so every
       folder is a peer of Recents on one continuous list. What keeps a long
       folder from burying the ones below it is the per-folder Show more page
       (see folder-sessions-list.vue), not a scrollbar. -->
  <div
    v-if="currentBotId"
    class="px-2 pb-0.5"
  >
    <!-- 「文件夹」区头与「最近」是同一个组头组件(group-header.vue):
         整行点击折叠整个文件夹区,chevron 与 +(新建文件夹)都 hover 显现。
         可折叠的是区头下的每个 folder 自己,与区头互不干扰。 -->
    <GroupHeader
      :label="t('chat.folders')"
      :expanded="!sectionCollapsed"
      :label-class="sectionLabelClass"
      compact
      class="mt-1"
      @toggle="toggleSectionCollapsed"
    >
      <TextButton
        :class="rowActionRevealClass"
        :aria-label="t('bots.folders.create')"
        @click.stop="createDialogOpen = true"
      >
        <Plus />
      </TextButton>
    </GroupHeader>

    <div
      v-if="!sectionCollapsed"
      class="pt-0.5"
    >
      <template
        v-for="folder in liveFolders"
        :key="folder.id"
      >
        <div class="pb-0.5">
          <GroupHeader
            :label="folder.name ?? ''"
            :expanded="isExpanded(folder.id ?? '')"
            :label-class="folderRowLabelClass"
            hover-fill
            @toggle="toggleExpanded(folder.id ?? '')"
          >
            <template #leading>
              <!-- 图标静态:展开态交给 chevron 表达,不再做 Folder/FolderOpen
                   互换(两个状态指示器并存是冗余)。 -->
              <Folder class="mr-2 size-4 shrink-0 text-muted-foreground" />
            </template>
            <!-- Menu first, plus last: the new-session plus takes the
                 rightmost slot so it lines up with the header's add button. -->
            <DropdownMenu>
              <DropdownMenuTrigger as-child>
                <TextButton
                  :class="rowActionRevealClass"
                  :aria-label="t('bots.folders.rowActions', { name: folder.name ?? '' })"
                  @click.stop
                >
                  <MoreHorizontal />
                </TextButton>
              </DropdownMenuTrigger>
              <DropdownMenuContent
                align="end"
                @click.stop
              >
                <DropdownMenuItem @select="openRenameDialog(folder)">
                  <Pencil class="mr-2 size-3.5" />
                  {{ t('bots.folders.rename') }}
                </DropdownMenuItem>
                <DropdownMenuItem @select="confirmArchive(folder)">
                  <Archive class="mr-2 size-3.5" />
                  {{ t('bots.folders.archive') }}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
            <TextButton
              :class="rowActionRevealClass"
              :aria-label="t('chat.folderNewSession', { name: folder.name ?? '' })"
              @click.stop="startFolderSession(folder.id ?? '')"
            >
              <Plus />
            </TextButton>
          </GroupHeader>
        </div>
        <FolderSessionsList
          v-if="isExpanded(folder.id ?? '')"
          :workdir-id="folder.id ?? ''"
          @select="handleSelect"
          @open-new-tab="handleOpenNewTab"
          @rename="sessionDialogs?.openRename($event)"
          @delete="sessionDialogs?.openDelete($event, { fallbackMode: 'recent' })"
        />
      </template>
    </div>

    <FolderCreateDialog
      v-model:open="createDialogOpen"
      :bot-id="currentBotId"
    />

    <ConfirmDeleteDialog
      v-model:open="archiveDialogOpen"
      :title="t('bots.folders.archiveTitle')"
      :description="t('bots.folders.archiveDescription', { name: pendingArchive?.name ?? '' })"
      :cancel-label="t('common.cancel')"
      :confirm-label="t('bots.folders.archive')"
      :loading="archiving"
      @confirm="handleArchive"
    />

    <Dialog v-model:open="renameDialogOpen">
      <DialogContent class="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{{ t('bots.folders.renameTitle') }}</DialogTitle>
        </DialogHeader>
        <form
          class="space-y-4"
          @submit.prevent="handleRename"
        >
          <Input
            v-model="renameTitle"
            :disabled="renaming"
            autofocus
          />
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              :disabled="renaming"
              @click="renameDialogOpen = false"
            >
              {{ t('common.cancel') }}
            </Button>
            <Button
              type="submit"
              :disabled="!renameTitle.trim()"
              :loading="renaming"
            >
              {{ t('common.confirm') }}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>

    <SessionDialogs ref="sessionDialogs" />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { useLocalStorage } from '@vueuse/core'
import { Archive, Folder, MoreHorizontal, Pencil, Plus } from 'lucide-vue-next'
import {
  Button,
  ConfirmDeleteDialog,
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
  Input,
  TextButton,
  toast,
} from '@felinic/ui'
import { useChatStore } from '@/store/chat-list'
import { useWorkdirsStore } from '@/store/workdirs'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { archiveWorkdir, renameWorkdir, type BotWorkdir } from '@/composables/api/useWorkdirs'
import type { SessionSummary } from '@/composables/api/useChat'
import { resolveApiErrorMessage } from '@/utils/api-error'
import SessionDialogs from './session-dialogs.vue'
import FolderSessionsList from './folder-sessions-list.vue'
import FolderCreateDialog from './folder-create-dialog.vue'
import GroupHeader from './group-header.vue'

const { t } = useI18n()
const chatStore = useChatStore()
const workdirsStore = useWorkdirsStore()
const workspaceTabs = useWorkspaceTabsStore()
const { currentBotId } = storeToRefs(chatStore)

// 区头 label 沿用改造前 TextButton 的渲染:静态 muted、hover 行变 foreground
// (旧区头是 ghost TextButton,text-muted-foreground hover:text-foreground)
const sectionLabelClass = 'text-xs font-[550] tracking-[-0.02em] text-muted-foreground transition-colors group-hover/group-header:text-foreground' /* ui-allow-style */

// folder 行的 label 沿用改造前的列表行字型(前景 text-control)
const folderRowLabelClass = 'text-control text-foreground'

// 组头 trailing 动作的显现类:menu trigger 额外带 data-[state=open]——reka
// 菜单打开时页面 pointer-events 被关,组头 hover 失效,trigger 靠自己的
// data-[state=open] 保持显现(见 group-header.vue 头注释)。
const rowActionRevealClass = 'opacity-0 group-hover/group-header:opacity-100 focus-visible:opacity-100 data-[state=open]:opacity-100'

const sessionDialogs = ref<InstanceType<typeof SessionDialogs> | null>(null)

watch(currentBotId, (botId) => {
  if (botId) void workdirsStore.ensureWorkdirs(botId)
}, { immediate: true })

const liveFolders = computed(() => (
  workdirsStore.workdirsFor(currentBotId.value).filter(folder => !folder.archived && !!folder.id)
))

// Section fold + per-folder expand state, per bot. Persisted: both are
// reading preferences, not transient UI state. Folders start collapsed.
const sectionCollapsedByBot = useLocalStorage<Record<string, boolean>>(
  'workspace-sidebar-folders-collapsed',
  {},
)
const sectionCollapsed = computed(() => sectionCollapsedByBot.value[currentBotId.value ?? ''] === true)
function toggleSectionCollapsed() {
  const botId = (currentBotId.value ?? '').trim()
  if (!botId) return
  sectionCollapsedByBot.value = {
    ...sectionCollapsedByBot.value,
    [botId]: !sectionCollapsed.value,
  }
}

// Per-folder expand state, per bot. Persisted: a reading preference, not
// transient UI state. Folders start collapsed.
const expandedByBot = useLocalStorage<Record<string, string[]>>(
  'workspace-sidebar-expanded-folders',
  {},
)
const expanded = computed(() => new Set(expandedByBot.value[currentBotId.value ?? ''] ?? []))
function isExpanded(workdirId: string): boolean {
  return expanded.value.has(workdirId)
}
function toggleExpanded(workdirId: string) {
  const botId = (currentBotId.value ?? '').trim()
  if (!botId || !workdirId) return
  const current = new Set(expandedByBot.value[botId] ?? [])
  if (current.has(workdirId)) current.delete(workdirId)
  else current.add(workdirId)
  expandedByBot.value = { ...expandedByBot.value, [botId]: [...current] }
}

// Expanded folders (including ones restored from the persisted preference)
// fetch their first page once. ensureWorkdirSessions is idempotent, so this
// can fire freely as the folder list or the expanded set changes.
watch(
  [currentBotId, expanded, liveFolders] as const,
  () => {
    if (!currentBotId.value) return
    for (const folder of liveFolders.value) {
      const workdirId = folder.id ?? ''
      if (workdirId && expanded.value.has(workdirId)) {
        void chatStore.ensureWorkdirSessions(workdirId)
      }
    }
  },
  { immediate: true },
)

function handleSelect(session: SessionSummary) {
  // Same contract as recents.vue: pass the raw title, let the tab store
  // derive the fallback (incl. channel conversation name).
  workspaceTabs.openSessionChat({
    sessionId: session.id,
    title: (session.title ?? '').trim(),
  })
}

function handleOpenNewTab(session: SessionSummary) {
  workspaceTabs.openSessionChatPinned({
    sessionId: session.id,
    title: (session.title ?? '').trim(),
  })
}

// Starting a session from a folder makes that workdir the bot's working
// directory — the draft composer shows the binding and the created session
// lands in this folder.
function startFolderSession(workdirId: string) {
  const botId = (currentBotId.value ?? '').trim()
  if (!botId || !workdirId) return
  workdirsStore.setWorkingWorkdir(botId, workdirId)
  workspaceTabs.openDraftChat({ title: t('chat.newSession'), explicitSelection: false })
}

const createDialogOpen = ref(false)

const renameDialogOpen = ref(false)
const renaming = ref(false)
const renameTitle = ref('')
const pendingRename = ref<BotWorkdir | null>(null)

function openRenameDialog(folder: BotWorkdir) {
  pendingRename.value = folder
  renameTitle.value = folder.name ?? ''
  renameDialogOpen.value = true
}

async function handleRename() {
  const botId = (currentBotId.value ?? '').trim()
  const target = pendingRename.value
  const name = renameTitle.value.trim()
  if (!botId || !target?.id || !name || renaming.value) return
  renaming.value = true
  try {
    await renameWorkdir(botId, target.id, name)
    await workdirsStore.refreshWorkdirs(botId)
    renameDialogOpen.value = false
    pendingRename.value = null
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.folders.renameFailed')))
  } finally {
    renaming.value = false
  }
}

const archiveDialogOpen = ref(false)
const archiving = ref(false)
const pendingArchive = ref<BotWorkdir | null>(null)

function confirmArchive(folder: BotWorkdir) {
  pendingArchive.value = folder
  archiveDialogOpen.value = true
}

async function handleArchive() {
  const botId = (currentBotId.value ?? '').trim()
  const target = pendingArchive.value
  if (!botId || !target?.id || archiving.value) return
  archiving.value = true
  try {
    await archiveWorkdir(botId, target.id)
    await workdirsStore.refreshWorkdirs(botId)
    archiveDialogOpen.value = false
    pendingArchive.value = null
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.folders.archiveFailed')))
  } finally {
    archiving.value = false
  }
}
</script>
