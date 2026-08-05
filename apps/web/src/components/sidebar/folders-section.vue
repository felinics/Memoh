<template>
  <!-- Folders — a sidebar section that is a SIBLING of Recents, never a
       group inside it: folders organize workdir-bound chats, Recents keeps
       the ungrouped timeline. Bounded height so a long folder list can never
       squeeze the Recents list out of the panel. -->
  <div
    v-if="currentBotId"
    class="flex min-h-0 shrink-0 flex-col px-2 pb-0.5"
  >
    <div class="flex items-center pt-1 pr-1">
      <!-- Same header affordance as the Recents mode switcher: the label is a
           TextButton with a tight trailing chevron; clicking it folds the
           whole section. -->
      <TextButton
        :class="sectionHeaderClass"
        @click="toggleSectionCollapsed"
      >
        {{ t('chat.folders') }}
        <ChevronDown
          class="size-2.5 transition-transform"
          :class="sectionCollapsed ? '-rotate-90' : ''"
        />
      </TextButton>
      <span class="min-w-0 flex-1" />
      <!-- The add button sits at the far right, sharing one column with the
           folder rows' new-session plus (their rightmost trailing slot). -->
      <div :class="sectionTrailingClass">
        <TextButton
          :aria-label="t('bots.folders.create')"
          @click="createDialogOpen = true"
        >
          <Plus />
        </TextButton>
      </div>
    </div>

    <div
      v-if="!sectionCollapsed"
      class="sidebar-scroll max-h-56 overflow-y-auto pr-1 pt-0.5"
    >
      <template
        v-for="folder in liveFolders"
        :key="folder.id"
      >
        <!-- Folder rows share the session rows' geometry (34px pill, 11px
             gutter, sidebar hover fill) so the two lists read as one system.
             The row is a div[role=button] so the hover-revealed actions can
             be real buttons inside it. -->
        <div class="pb-0.5">
          <div
            role="button"
            tabindex="0"
            :class="folderRowClass"
            @click="toggleExpanded(folder.id ?? '')"
            @keydown.enter.prevent="toggleExpanded(folder.id ?? '')"
            @keydown.space.prevent="toggleExpanded(folder.id ?? '')"
          >
            <component
              :is="isExpanded(folder.id ?? '') ? FolderOpen : Folder"
              class="mr-2 size-4 shrink-0 text-muted-foreground"
            />
            <span class="min-w-0 flex-1 truncate text-control text-foreground">{{ folder.name }}</span>
            <!-- Menu first, plus last: the new-session plus takes the
                 rightmost slot so it lines up with the header's add button. -->
            <div class="ml-1.5 flex shrink-0 items-center">
              <DropdownMenu>
                <DropdownMenuTrigger as-child>
                  <TextButton
                    class="opacity-0 focus-visible:opacity-100 group-hover/folder:opacity-100 data-[state=open]:opacity-100"
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
                class="opacity-0 focus-visible:opacity-100 group-hover/folder:opacity-100"
                :aria-label="t('chat.folderNewSession', { name: folder.name ?? '' })"
                @click.stop="startFolderSession(folder.id ?? '')"
              >
                <Plus />
              </TextButton>
            </div>
          </div>
        </div>
        <template v-if="isExpanded(folder.id ?? '')">
          <div
            v-for="session in sessionsOf(folder.id ?? '')"
            :key="session.id"
            class="pb-0.5 pl-4"
          >
            <SessionItem
              :session="session"
              :is-active="sessionId === session.id"
              :streaming="chatStore.isSessionStreaming(currentBotId, session.id)"
              @select="handleSelect"
              @open-new-tab="handleOpenNewTab"
              @rename="sessionDialogs?.openRename($event)"
              @delete="sessionDialogs?.openDelete($event, { fallbackMode: 'recent' })"
            />
          </div>
          <!-- The folder pages the workdir-filtered endpoint, so an old
               folder's chats stay reachable even when they fall outside the
               Recents timeline's loaded pages. -->
          <div
            v-if="pagingState(folder.id ?? '').loading"
            class="flex justify-center py-2 pl-4"
          >
            <Spinner class="size-4" />
          </div>
          <div
            v-else-if="pagingState(folder.id ?? '').hasMore"
            class="pb-0.5 pl-4"
          >
            <TextButton
              class="text-xs"
              @click="chatStore.loadMoreWorkdirSessions(folder.id ?? '')"
            >
              {{ t('chat.showMore') }}
            </TextButton>
          </div>
          <div
            v-else-if="pagingState(folder.id ?? '').loaded && sessionsOf(folder.id ?? '').length === 0"
            class="px-3 py-2 pl-4 text-xs text-muted-foreground"
          >
            {{ t('chat.noSessions') }}
          </div>
        </template>
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
import { Archive, ChevronDown, Folder, FolderOpen, MoreHorizontal, Pencil, Plus } from 'lucide-vue-next'
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
  Spinner,
  TextButton,
  toast,
} from '@felinic/ui'
import { useChatStore } from '@/store/chat-list'
import { useWorkdirsStore } from '@/store/workdirs'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { archiveWorkdir, renameWorkdir, type BotWorkdir } from '@/composables/api/useWorkdirs'
import type { SessionSummary } from '@/composables/api/useChat'
import { resolveApiErrorMessage } from '@/utils/api-error'
import SessionItem from './session-item.vue'
import SessionDialogs from './session-dialogs.vue'
import FolderCreateDialog from './folder-create-dialog.vue'
import '@/styles/sidebar-scroll.css'

const { t } = useI18n()
const chatStore = useChatStore()
const workdirsStore = useWorkdirsStore()
const workspaceTabs = useWorkspaceTabsStore()
const { sessionId, currentBotId } = storeToRefs(chatStore)

// Same header type as the Recents mode switcher so the two sibling section
// titles read identically; the 11px inset aligns the sidebar's 19px
// icon/label column (see panel-header.vue).
const sectionHeaderClass = 'text-xs font-[550] tracking-[-0.02em] pl-[11px] select-none' /* ui-allow-px: aligns the sidebar 19px label column (see panel-header.vue) */ /* ui-allow-style */

// Header trailing inset mirrors the folder rows' 11px inner gutter so the
// header plus and the rows' plus land in one column.
const sectionTrailingClass = 'flex shrink-0 items-center pr-[11px]' /* ui-allow-px: matches the folder rows' 11px trailing gutter */

// Folder rows copy session-item.vue's row geometry (34px pill, 11px gutter,
// sidebar hover fill) so folders and session rows read as one list system.
const folderRowClass = 'group/folder relative flex w-full min-h-[2.125rem] cursor-pointer select-none items-center rounded-[9px] px-[11px] text-left transition-colors hover:bg-[color:var(--sidebar-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring' /* ui-allow-px: matches session-item.vue's 11px sidebar row gutter */ /* ui-allow-style: sidebar rows are a deliberately local row system (see ui-owners) — same hover token as session-item.vue */

const sessionDialogs = ref<InstanceType<typeof SessionDialogs> | null>(null)

watch(currentBotId, (botId) => {
  if (botId) void workdirsStore.ensureWorkdirs(botId)
}, { immediate: true })

const liveFolders = computed(() => (
  workdirsStore.workdirsFor(currentBotId.value).filter(folder => !folder.archived && !!folder.id)
))

// Folder rows come from the store's per-workdir paging, not from filtering the
// shared Recents list — that list pages the whole bot timeline, so a folder
// older than the loaded pages would read as empty with nothing to expand into.
function sessionsOf(workdirId: string): SessionSummary[] {
  return chatStore.workdirSessionsFor(workdirId)
}

function pagingState(workdirId: string) {
  return chatStore.workdirSessionsState(workdirId)
}

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
  workspaceTabs.openSessionChat({
    sessionId: session.id,
    title: (session.title ?? '').trim() || t('chat.untitledSession'),
  })
}

function handleOpenNewTab(session: SessionSummary) {
  workspaceTabs.openSessionChatPinned({
    sessionId: session.id,
    title: (session.title ?? '').trim() || t('chat.untitledSession'),
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
