<template>
  <div
    class="group/tree relative flex flex-col h-full min-w-0"
    v-on="dropHandlers"
  >
    <!-- Section header: aligns with the Chat ("Quick Actions") and Schedule
         group headers via SidebarPanelHeader (same mt-2 top inset, same label
         type) so the first label in each capsule shares one baseline.
         Horizontal indent (mx-1 pl-[11px]) stays on the CONTAINER to sit over
         the tree's chevron column; do not switch to px-2 or the header text
         drifts off the icon column. -->
    <SidebarPanelHeader
      class="group/pane-header min-h-[1.6875rem] mx-1 pl-[11px] mt-2"
      :label="botName || t('bots.files.panelTitle')"
    >
      <div class="flex items-center gap-0.5 pr-1 opacity-0 transition-opacity duration-150 group-hover/pane-header:opacity-100">
        <Button
          v-if="canWrite"
          variant="ghost"
          class="size-[26px] shrink-0 p-0 text-muted-foreground/70 hover:text-foreground"
          :disabled="operationLoading"
          :title="t('bots.files.newFile')"
          @click="openNewFileDialog(rootPath)"
        >
          <FilePlus class="size-4" />
        </Button>
        <Button
          v-if="canWrite"
          variant="ghost"
          class="size-[26px] shrink-0 p-0 text-muted-foreground/70 hover:text-foreground"
          :disabled="operationLoading"
          :title="t('bots.files.newFolder')"
          @click="openMkdirDialog(rootPath)"
        >
          <FolderPlus class="size-4" />
        </Button>
        <DropdownMenu v-if="canWrite">
          <DropdownMenuTrigger as-child>
            <Button
              variant="ghost"
              class="size-[26px] shrink-0 p-0 text-muted-foreground/70 hover:text-foreground"
              :disabled="operationLoading"
              :title="t('bots.files.upload')"
            >
              <Upload class="size-4" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem @select="triggerUpload(rootPath)">
              <Upload class="mr-2 size-3.5" />
              {{ t('bots.files.upload') }}
            </DropdownMenuItem>
            <DropdownMenuItem @select="triggerDirectoryUpload">
              <CloudUpload class="mr-2 size-3.5" />
              {{ t('bots.files.uploadFolder') }}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
        <Button
          variant="ghost"
          class="size-[26px] shrink-0 p-0 text-muted-foreground/70 hover:text-foreground"
          :title="t('common.refresh')"
          @click="reloadAndBroadcast"
        >
          <RefreshCw class="size-4" />
        </Button>
      </div>
    </SidebarPanelHeader>
    <!-- Batch ops bar: shown when items are selected -->
    <div
      v-if="selectedCount > 0"
      class="flex shrink-0 items-center gap-1 border-b border-border px-2 h-7"
    >
      <span class="min-w-0 flex-1 truncate text-caption text-muted-foreground">
        {{ t('bots.files.selectedCount', { count: selectedCount }) }}
      </span>
      <Button
        v-if="canWrite"
        variant="ghost"
        size="sm"
        class="size-6 p-0"
        :disabled="operationLoading"
        :title="t('bots.files.download')"
        @click="handleBatchDownload"
      >
        <Download class="size-3.5" />
      </Button>
      <Button
        variant="ghost"
        size="sm"
        class="size-6 p-0 text-destructive hover:text-destructive"
        :disabled="operationLoading"
        :title="t('bots.files.delete')"
        @click="openBatchDeleteDialog"
      >
        <Trash2 class="size-3.5" />
      </Button>
      <Button
        variant="ghost"
        size="sm"
        class="size-6 p-0"
        :title="t('bots.files.doneSelecting')"
        @click="toggleSelectionMode"
      >
        <X class="size-3.5" />
      </Button>
    </div>

    <input
      ref="uploadInputRef"
      type="file"
      class="hidden"
      :disabled="operationLoading || !canWrite"
      @change="handleUpload"
    >
    <input
      ref="directoryInputRef"
      type="file"
      class="hidden"
      multiple
      webkitdirectory
      :disabled="operationLoading || !canWrite"
      @change="handleDirectoryInputUpload"
    >

    <div
      v-if="uploadStatus"
      class="flex shrink-0 items-center gap-1 border-b border-border px-2 py-1.5 text-caption"
    >
      <span class="min-w-0 truncate text-muted-foreground">
        {{ uploadStatus }}
      </span>
    </div>

    <div class="flex-1 min-h-0 relative">
      <div class="absolute inset-0">
        <ContextMenu>
          <ContextMenuTrigger as-child>
            <ScrollArea class="h-full">
              <FileTree />
            </ScrollArea>
          </ContextMenuTrigger>
          <ContextMenuContent>
            <template v-if="canWrite">
              <ContextMenuItem @select="openNewFileDialog(rootPath)">
                <FilePlus class="mr-2 size-3.5" />
                {{ t('bots.files.newFile') }}
              </ContextMenuItem>
              <ContextMenuItem @select="openMkdirDialog(rootPath)">
                <FolderPlus class="mr-2 size-3.5" />
                {{ t('bots.files.newFolder') }}
              </ContextMenuItem>
              <ContextMenuSeparator />
              <ContextMenuItem @select="triggerUpload(rootPath)">
                <Upload class="mr-2 size-3.5" />
                {{ t('bots.files.upload') }}
              </ContextMenuItem>
              <ContextMenuItem @select="triggerDirectoryUpload">
                <CloudUpload class="mr-2 size-3.5" />
                {{ t('bots.files.uploadFolder') }}
              </ContextMenuItem>
              <ContextMenuSeparator />
              <ContextMenuItem @select="toggleSelectionMode">
                <ListChecks class="mr-2 size-3.5" />
                {{ t('bots.files.selectMode') }}
              </ContextMenuItem>
              <ContextMenuSeparator />
            </template>
            <ContextMenuItem @select="reloadAndBroadcast">
              <RefreshCw class="mr-2 size-3.5" />
              {{ t('common.refresh') }}
            </ContextMenuItem>
          </ContextMenuContent>
        </ContextMenu>
      </div>
    </div>

    <Dialog v-model:open="newFileDialogOpen">
      <DialogContent class="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{{ t('bots.files.newFile') }}</DialogTitle>
        </DialogHeader>
        <Input
          v-model="newFileName"
          :placeholder="t('bots.files.fileNamePlaceholder')"
          :disabled="newFileLoading"
          @keydown.enter.prevent="handleNewFile"
        />
        <DialogFooter>
          <Button
            variant="outline"
            :disabled="newFileLoading"
            @click="newFileDialogOpen = false"
          >
            {{ t('common.cancel') }}
          </Button>
          <Button
            :disabled="!newFileName.trim()"
            :loading="newFileLoading"
            @click="handleNewFile"
          >
            {{ t('common.confirm') }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <Dialog v-model:open="mkdirDialogOpen">
      <DialogContent class="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{{ t('bots.files.newFolder') }}</DialogTitle>
        </DialogHeader>
        <Input
          v-model="mkdirName"
          :placeholder="t('bots.files.folderNamePlaceholder')"
          :disabled="mkdirLoading"
          @keydown.enter.prevent="handleMkdir"
        />
        <DialogFooter>
          <Button
            variant="outline"
            :disabled="mkdirLoading"
            @click="mkdirDialogOpen = false"
          >
            {{ t('common.cancel') }}
          </Button>
          <Button
            :disabled="!mkdirName.trim()"
            :loading="mkdirLoading"
            @click="handleMkdir"
          >
            {{ t('common.confirm') }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <Dialog v-model:open="renameDialogOpen">
      <DialogContent class="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{{ t('bots.files.rename') }}</DialogTitle>
        </DialogHeader>
        <Input
          v-model="renameNewName"
          :placeholder="t('bots.files.newNamePlaceholder')"
          :disabled="renameLoading"
          @keydown.enter.prevent="handleRename"
        />
        <DialogFooter>
          <Button
            variant="outline"
            :disabled="renameLoading"
            @click="renameDialogOpen = false"
          >
            {{ t('common.cancel') }}
          </Button>
          <Button
            :disabled="!renameNewName.trim()"
            :loading="renameLoading"
            @click="handleRename"
          >
            {{ t('common.confirm') }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <Dialog v-model:open="deleteDialogOpen">
      <DialogContent class="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{{ t('bots.files.confirmDelete') }}</DialogTitle>
        </DialogHeader>
        <p class="text-xs text-muted-foreground">
          {{ t('bots.files.confirmDeleteMessage', { name: deleteTarget?.name ?? '' }) }}
        </p>
        <DialogFooter>
          <Button
            variant="outline"
            :disabled="deleteLoading"
            @click="deleteDialogOpen = false"
          >
            {{ t('common.cancel') }}
          </Button>
          <Button
            variant="destructive"
            :loading="deleteLoading"
            @click="handleDelete"
          >
            {{ t('bots.files.delete') }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <Dialog v-model:open="batchDeleteDialogOpen">
      <DialogContent class="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{{ t('bots.files.confirmDelete') }}</DialogTitle>
        </DialogHeader>
        <p class="text-xs text-muted-foreground">
          {{ t('bots.files.confirmBatchDeleteMessage', { count: selectedCount }) }}
        </p>
        <DialogFooter>
          <Button
            variant="outline"
            :disabled="batchDeleteLoading"
            @click="batchDeleteDialogOpen = false"
          >
            {{ t('common.cancel') }}
          </Button>
          <Button
            variant="destructive"
            :loading="batchDeleteLoading"
            @click="handleBatchDelete"
          >
            {{ t('bots.files.delete') }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <!-- Region-scoped drop feedback. The panel is the whole target: dropping
         anywhere in it lands in the workspace root, NOT in the folder under the
         cursor — the tree has no notion of a "current directory" (navigateTo
         only reveals a path), so per-folder aim would have to be invented here
         and would silently disagree with the toolbar's Upload, which also
         targets the root. -->
    <FileDropOverlay
      :active="dropActive"
      :bounds="dropBounds"
      :icon="Upload"
      :label="t('bots.files.dropToUpload')"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, provide, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from '@felinic/ui'
import { CloudUpload, Download, FilePlus, FolderPlus, ListChecks, RefreshCw, Trash2, Upload, X } from 'lucide-vue-next'
import {
  Button,
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
  ContextMenuTrigger,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
  Input,
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
  ScrollArea,
} from '@felinic/ui'
import {
  getBotsByBotIdContainerFsList,
  postBotsByBotIdContainerFsUpload,
  postBotsByBotIdContainerFsMkdir,
  postBotsByBotIdContainerFsDelete,
  postBotsByBotIdContainerFsRename,
  postBotsByBotIdContainerFsWrite,
} from '@memohai/sdk'
import type { HandlersFsFileInfo } from '@memohai/sdk'
import { isApiErrorCode, resolveApiErrorMessage } from '@/utils/api-error'
import { sdkApiUrl, sdkAuthQuery } from '@/lib/api-client'
import { joinPath, parentPath } from '@/components/file-manager/utils'
import FileTree from '@/components/file-manager/file-tree.vue'
import SidebarPanelHeader from './panel-header.vue'
import FileDropOverlay from '@/components/file-drop-overlay/index.vue'
import { useFileDropZone } from '@/composables/useFileDropZone'
import { readDroppedContents, type DroppedContents } from '@/utils/dropped-files'
import { FileTreeKey } from '@/components/file-manager/file-tree-context'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { useChatStore } from '@/store/chat-list'
import { storeToRefs } from 'pinia'

const props = withDefaults(defineProps<{
  botId: string
  canWrite?: boolean
  botName?: string
  active?: boolean
}>(), {
  canWrite: false,
  botName: '',
  active: true,
})

const FILE_TREE_POLL_MS = 2_000

const { t } = useI18n()
const workspaceTabs = useWorkspaceTabsStore()
const chatStore = useChatStore()
const { activeId } = storeToRefs(workspaceTabs)
const { fsChangedAt } = storeToRefs(chatStore)
const canWrite = computed(() => props.canWrite)

const rootPath = '/data'

// Tree state shared with FileTree / FileTreeNode via provide.
const refreshKey = ref(0)
const revealPath = ref<string | null>(null)
const activePath = computed(() => {
  const id = activeId.value
  return id && id.startsWith('file:') ? id.slice('file:'.length) : null
})

const uploadInputRef = ref<HTMLInputElement>()
const directoryInputRef = ref<HTMLInputElement>()
const uploadStatus = ref('')
const uploadBusy = ref(false)
const batchArchiveLoading = ref(false)
const extractLoading = ref(false)
const batchDeleteDialogOpen = ref(false)
const batchDeleteLoading = ref(false)
const selectionMode = ref(false)
const selectedEntries = ref<Map<string, HandlersFsFileInfo>>(new Map())
const selectedCount = computed(() => selectedEntries.value.size)
const operationLoading = computed(() => uploadBusy.value || batchArchiveLoading.value || extractLoading.value || batchDeleteLoading.value)

interface DirectoryUploadFile {
  file: File
  relativePath: string
}

interface DirectoryUploadPayload {
  rootName: string
  files: DirectoryUploadFile[]
  directories: string[]
}

interface FileSystemDirectoryHandleLike {
  name: string
  entries(): AsyncIterableIterator<[string, FileSystemHandleLike]>
}

interface FileSystemFileHandleLike {
  name: string
  getFile(): Promise<File>
}

type FileSystemHandleLike =
  | (FileSystemDirectoryHandleLike & { kind: 'directory' })
  | (FileSystemFileHandleLike & { kind: 'file' })

function isTransientWorkspaceError(error: unknown): boolean {
  if (isApiErrorCode(error, 'workspace.unreachable')) return true

  // Desktop can connect to older hosted servers that only expose a message.
  const detail = resolveApiErrorMessage(error, '').toLowerCase()
  return detail.includes('workspace is not reachable')
    || detail.includes('container not reachable')
    || detail.includes('unavailable')
    || detail.includes('client connection is closing')
    || detail.includes('transport is closing')
}

function wait(ms: number): Promise<void> {
  return new Promise(resolve => window.setTimeout(resolve, ms))
}

function authHeaders(): HeadersInit {
  const headers: Record<string, string> = {}
  const token = localStorage.getItem('token')?.trim()
  if (token) headers.Authorization = `Bearer ${token}`
  return headers
}

async function readErrorMessage(response: Response, fallback: string): Promise<string> {
  try {
    const data = await response.json() as { message?: string, error?: string }
    return data.message || data.error || fallback
  } catch {
    const text = await response.text().catch(() => '')
    return text || fallback
  }
}

function downloadBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  window.setTimeout(() => URL.revokeObjectURL(url), 0)
}

// List a single directory's entries (with a short retry for transient workspace
// errors). Used by the tree per folder; failures surface as a toast and an empty
// folder rather than breaking the whole tree.
async function listDirectory(path: string): Promise<HandlersFsFileInfo[]> {
  if (!props.botId) return []
  let lastError: unknown
  for (let attempt = 0; attempt < 3; attempt++) {
    try {
      const { data } = await getBotsByBotIdContainerFsList({
        path: { bot_id: props.botId },
        query: { path },
        throwOnError: true,
      })
      return data.entries ?? []
    } catch (error) {
      lastError = error
      if (attempt < 2 && isTransientWorkspaceError(error)) {
        await wait(500 * (attempt + 1))
        continue
      }
      throw error
    }
  }
  throw lastError
}

async function safeListDirectory(path: string): Promise<HandlersFsFileInfo[]> {
  try {
    return await listDirectory(path)
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.files.loadFailed')))
    return []
  }
}

function reload() {
  refreshKey.value++
}

// User-driven mutations call this to refresh the local tree AND tell the chat
// store so any open file viewer / preview reloads its content. Keep this OFF
// the fsChangedAt watcher path or the watcher would re-bump fsChangedAt and
// loop forever.
function reloadAndBroadcast() {
  reload()
  chatStore.markFsChanged()
}

let treePollTimer: ReturnType<typeof setInterval> | null = null

function isDocumentVisible(): boolean {
  return typeof document === 'undefined' || document.visibilityState === 'visible'
}

function pollTree() {
  if (!props.botId || !props.active || !isDocumentVisible()) return
  reload()
}

function startTreePoll() {
  if (treePollTimer !== null || !props.botId || !props.active) return
  treePollTimer = window.setInterval(pollTree, FILE_TREE_POLL_MS)
}

function stopTreePoll() {
  if (treePollTimer === null) return
  window.clearInterval(treePollTimer)
  treePollTimer = null
}

function handleVisibilityChange() {
  if (!props.active || !isDocumentVisible()) {
    stopTreePoll()
    return
  }
  reload()
  startTreePoll()
}

// Reveal a path in the tree (expand its ancestors + scroll into view). Used by
// the store's openFilesAt deep-link.
function navigateTo(path: string) {
  const target = (path ?? '').trim()
  if (!target) return
  // Re-assign even if unchanged so repeated requests still re-trigger reveal.
  revealPath.value = null
  void Promise.resolve().then(() => {
    revealPath.value = target
  })
}

function handleOpenFile(entry: HandlersFsFileInfo) {
  if (!entry.path) return
  workspaceTabs.openFile(entry.path)
}

function handleOpenFilePinned(entry: HandlersFsFileInfo) {
  if (!entry.path) return
  workspaceTabs.openFilePinned(entry.path)
}

function handleOpenFileToSide(entry: HandlersFsFileInfo) {
  if (!entry.path) return
  workspaceTabs.openFileToSide(entry.path)
}

// ---- upload --------------------------------------------------------------

const uploadTarget = ref(rootPath)

function triggerUpload(target: string) {
  if (!props.canWrite) return
  uploadTarget.value = target || rootPath
  uploadInputRef.value?.click()
}

async function triggerDirectoryUpload() {
  if (!props.canWrite) return
  uploadTarget.value = rootPath
  const picker = (window as unknown as {
    showDirectoryPicker?: () => Promise<FileSystemDirectoryHandleLike>
  }).showDirectoryPicker

  if (picker) {
    try {
      const handle = await picker()
      await uploadDirectoryPayload(await collectDirectoryHandle(handle))
      return
    } catch (error) {
      if (error instanceof DOMException && error.name === 'AbortError') return
      toast.error(resolveApiErrorMessage(error, t('bots.files.uploadFailed')))
      return
    }
  }

  directoryInputRef.value?.click()
}

function joinRelativePath(...parts: string[]): string {
  return parts
    .filter(Boolean)
    .join('/')
    .replace(/\/+/g, '/')
    .replace(/^\/+|\/+$/g, '')
}

function parentRelativeDirs(relativePath: string): string[] {
  const parts = relativePath.split('/').filter(Boolean)
  parts.pop()
  const dirs: string[] = []
  let current = ''
  for (const part of parts) {
    current = joinRelativePath(current, part)
    dirs.push(current)
  }
  return dirs
}

async function collectDirectoryHandle(root: FileSystemDirectoryHandleLike): Promise<DirectoryUploadPayload> {
  const files: DirectoryUploadFile[] = []
  const directories = new Set<string>()

  async function walk(dir: FileSystemDirectoryHandleLike, prefix = '') {
    for await (const [name, handle] of dir.entries()) {
      const relativePath = joinRelativePath(prefix, name)
      if (handle.kind === 'directory') {
        directories.add(relativePath)
        await walk(handle, relativePath)
      } else {
        files.push({ file: await handle.getFile(), relativePath })
      }
    }
  }

  await walk(root)
  return {
    rootName: root.name || t('bots.files.uploadedFolderFallbackName'),
    files,
    directories: [...directories],
  }
}

function collectDirectoryInput(files: FileList): DirectoryUploadPayload | null {
  const uploadFiles = Array.from(files)
  if (uploadFiles.length === 0) return null
  const firstPath = uploadFiles[0]?.webkitRelativePath || uploadFiles[0]?.name || ''
  const rootName = firstPath.split('/').filter(Boolean)[0] || t('bots.files.uploadedFolderFallbackName')
  const directories = new Set<string>()
  const payloadFiles = uploadFiles.map((file) => {
    const rawRelativePath = file.webkitRelativePath || file.name
    const parts = rawRelativePath.split('/').filter(Boolean)
    const relativePath = joinRelativePath(...(parts[0] === rootName ? parts.slice(1) : parts))
    for (const dir of parentRelativeDirs(relativePath)) {
      directories.add(dir)
    }
    return { file, relativePath }
  }).filter(item => item.relativePath)
  return { rootName, files: payloadFiles, directories: [...directories] }
}

async function handleDirectoryInputUpload(event: Event) {
  const input = event.target as HTMLInputElement
  const payload = input.files ? collectDirectoryInput(input.files) : null
  if (!payload) return
  try {
    await uploadDirectoryPayload(payload)
  } finally {
    input.value = ''
  }
}

// Shared upload engine for every multi-entry upload (folder picker, drag-drop).
// Owns the busy gate and the progress line; the RESULT toast is the caller's,
// because only the caller knows whether the user dropped files, a folder, or
// both. Directories are created first and sequentially: a nested file upload
// fails if its parent doesn't exist yet, and the paths arrive parent-first.
async function runUploadBatch(
  directories: string[],
  files: { path: string, file: File }[],
): Promise<{ failed: number, total: number }> {
  const total = directories.length + files.length
  let completed = 0
  let failed = 0
  const updateProgress = () => {
    uploadStatus.value = t('bots.files.uploadProgress', { done: completed, total })
  }

  uploadBusy.value = true
  updateProgress()
  try {
    for (const dir of directories) {
      try {
        await postBotsByBotIdContainerFsMkdir({
          path: { bot_id: props.botId },
          body: { path: dir },
          throwOnError: true,
        })
      } catch {
        failed++
      } finally {
        completed++
        updateProgress()
      }
    }

    // Bounded fan-out: the bridge is a single container, so unbounded parallel
    // uploads of a large folder just trade throughput for connection pressure.
    let cursor = 0
    const concurrency = 4
    async function worker() {
      for (;;) {
        const index = cursor++
        const item = files[index]
        if (!item) return
        try {
          await postBotsByBotIdContainerFsUpload({
            path: { bot_id: props.botId },
            body: { path: item.path, file: item.file } as never,
            throwOnError: true,
          })
        } catch {
          failed++
        } finally {
          completed++
          updateProgress()
        }
      }
    }
    await Promise.all(Array.from({ length: Math.min(concurrency, files.length) }, () => worker()))
    return { failed, total }
  } finally {
    uploadBusy.value = false
    // Leave the final count visible for a beat — clearing it the instant the
    // last request lands reads as if nothing happened.
    window.setTimeout(() => {
      if (!uploadBusy.value) uploadStatus.value = ''
    }, 1500)
  }
}

async function uploadDirectoryPayload(payload: DirectoryUploadPayload) {
  if (!props.canWrite) return
  if (uploadBusy.value) return
  const rootName = payload.rootName.trim() || t('bots.files.uploadedFolderFallbackName')
  const destinationRoot = joinPath(uploadTarget.value, rootName)
  // '' materializes destinationRoot itself; parentRelativeDirs covers folders
  // that only exist implicitly in a file's path (the folder-input flow reports
  // no directory list of its own).
  const directories = [...new Set([
    '',
    ...payload.directories,
    ...payload.files.flatMap(file => parentRelativeDirs(file.relativePath)),
  ])].map(dir => joinPath(destinationRoot, dir))

  try {
    const { failed, total } = await runUploadBatch(
      directories,
      payload.files.map(item => ({ path: joinPath(destinationRoot, item.relativePath), file: item.file })),
    )
    if (failed > 0) {
      toast.error(t('bots.files.uploadPartialFailed', { failed, total }))
    } else {
      toast.success(t('bots.files.uploadFolderSuccess'))
    }
    reloadAndBroadcast()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.files.uploadFailed')))
  }
}

async function handleUpload(event: Event) {
  if (!props.canWrite) return
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return

  const destPath = joinPath(uploadTarget.value, file.name)
  try {
    await postBotsByBotIdContainerFsUpload({
      path: { bot_id: props.botId },
      body: { path: destPath, file } as never,
      throwOnError: true,
    })
    toast.success(t('bots.files.uploadSuccess'))
    if (uploadTarget.value !== rootPath) navigateTo(uploadTarget.value)
    reloadAndBroadcast()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.files.uploadFailed')))
  } finally {
    input.value = ''
  }
}

// ---- drag & drop ---------------------------------------------------------

// A single drop can carry loose files and whole folders at once, so everything
// is flattened into ONE batch: one progress line, one result toast. Anything
// less makes a mixed drop report itself as several unrelated uploads.
async function handleFilesDrop(transfer: DataTransfer) {
  let contents: DroppedContents
  try {
    contents = await readDroppedContents(transfer)
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.files.uploadFailed')))
    return
  }

  const directories = new Set<string>()
  const files: { path: string, file: File }[] = []

  for (const file of contents.files) {
    files.push({ path: joinPath(rootPath, file.name), file })
  }
  for (const folder of contents.folders) {
    const destinationRoot = joinPath(rootPath, folder.name)
    // '' materializes destinationRoot itself; parentRelativeDirs backfills any
    // ancestor the walk reported only through a file path.
    for (const dir of ['', ...folder.directories, ...folder.files.flatMap(item => parentRelativeDirs(item.relativePath))]) {
      directories.add(joinPath(destinationRoot, dir))
    }
    for (const item of folder.files) {
      files.push({ path: joinPath(destinationRoot, item.relativePath), file: item.file })
    }
  }

  // An empty folder tree still has directories to create; a drop with neither is
  // nothing the user can see (e.g. entries the browser refused to open).
  if (files.length === 0 && directories.size === 0) return

  try {
    const { failed, total } = await runUploadBatch([...directories], files)
    if (failed > 0) {
      toast.error(t('bots.files.uploadPartialFailed', { failed, total }))
    } else {
      toast.success(t('bots.files.uploadSuccess'))
    }
    reloadAndBroadcast()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.files.uploadFailed')))
  }
}

const { active: dropActive, bounds: dropBounds, handlers: dropHandlers } = useFileDropZone({
  // Read-only members and an upload already in flight both refuse the drop, so
  // the OS paints its no-drop cursor instead of an overlay promising an upload
  // that won't happen.
  disabled: () => !props.canWrite || operationLoading.value,
  onDrop: transfer => void handleFilesDrop(transfer),
})

// ---- new file ------------------------------------------------------------

const newFileDialogOpen = ref(false)
const newFileName = ref('')
const newFileLoading = ref(false)
const newFileTarget = ref(rootPath)

function openNewFileDialog(target: string) {
  if (!props.canWrite) return
  newFileTarget.value = target || rootPath
  newFileName.value = ''
  newFileDialogOpen.value = true
}

async function handleNewFile() {
  if (!props.canWrite) return
  const name = newFileName.value.trim()
  if (!name || newFileLoading.value) return

  newFileLoading.value = true
  try {
    const filePath = joinPath(newFileTarget.value, name)
    await postBotsByBotIdContainerFsWrite({
      path: { bot_id: props.botId },
      body: { path: filePath, content: '' },
      throwOnError: true,
    })
    newFileDialogOpen.value = false
    toast.success(t('bots.files.newFileSuccess'))
    reloadAndBroadcast()
    workspaceTabs.openFile(filePath)
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.files.newFileFailed')))
  } finally {
    newFileLoading.value = false
  }
}

// ---- mkdir ---------------------------------------------------------------

const mkdirDialogOpen = ref(false)
const mkdirName = ref('')
const mkdirLoading = ref(false)
const mkdirTarget = ref(rootPath)

function openMkdirDialog(target: string) {
  if (!props.canWrite) return
  mkdirTarget.value = target || rootPath
  mkdirName.value = ''
  mkdirDialogOpen.value = true
}

async function handleMkdir() {
  if (!props.canWrite) return
  const name = mkdirName.value.trim()
  if (!name || mkdirLoading.value) return

  mkdirLoading.value = true
  try {
    await postBotsByBotIdContainerFsMkdir({
      path: { bot_id: props.botId },
      body: { path: joinPath(mkdirTarget.value, name) },
      throwOnError: true,
    })
    mkdirDialogOpen.value = false
    toast.success(t('bots.files.mkdirSuccess'))
    if (mkdirTarget.value !== rootPath) navigateTo(mkdirTarget.value)
    reloadAndBroadcast()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.files.mkdirFailed')))
  } finally {
    mkdirLoading.value = false
  }
}

// ---- rename --------------------------------------------------------------

const renameDialogOpen = ref(false)
const renameTarget = ref<HandlersFsFileInfo | null>(null)
const renameNewName = ref('')
const renameLoading = ref(false)

function openRenameDialog(entry: HandlersFsFileInfo) {
  if (!props.canWrite) return
  renameTarget.value = entry
  renameNewName.value = entry.name ?? ''
  renameDialogOpen.value = true
}

async function handleRename() {
  if (!props.canWrite) return
  const target = renameTarget.value
  const newName = renameNewName.value.trim()
  if (!target || !target.path || !newName || renameLoading.value) return

  renameLoading.value = true
  try {
    await postBotsByBotIdContainerFsRename({
      path: { bot_id: props.botId },
      body: {
        oldPath: target.path,
        newPath: joinPath(parentPath(target.path), newName),
      },
      throwOnError: true,
    })
    renameDialogOpen.value = false
    toast.success(t('bots.files.renameSuccess'))
    reloadAndBroadcast()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.files.renameFailed')))
  } finally {
    renameLoading.value = false
  }
}

// ---- delete --------------------------------------------------------------

const deleteDialogOpen = ref(false)
const deleteTarget = ref<HandlersFsFileInfo | null>(null)
const deleteLoading = ref(false)

function openDeleteDialog(entry: HandlersFsFileInfo) {
  if (!props.canWrite) return
  deleteTarget.value = entry
  deleteDialogOpen.value = true
}

async function handleDelete() {
  if (!props.canWrite) return
  const target = deleteTarget.value
  if (!target || deleteLoading.value) return

  deleteLoading.value = true
  try {
    await postBotsByBotIdContainerFsDelete({
      path: { bot_id: props.botId },
      body: { path: target.path, recursive: target.isDir },
      throwOnError: true,
    })
    deleteDialogOpen.value = false
    toast.success(t('bots.files.deleteSuccess'))
    if (target.path) {
      const next = new Map(selectedEntries.value)
      next.delete(target.path)
      selectedEntries.value = next
    }
    reloadAndBroadcast()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.files.deleteFailed')))
  } finally {
    deleteLoading.value = false
  }
}

// ---- selection + batch ---------------------------------------------------

function isSelected(path: string): boolean {
  return selectedEntries.value.has(path)
}

function toggleSelection(entry: HandlersFsFileInfo, selected: boolean) {
  if (!entry.path) return
  const next = new Map(selectedEntries.value)
  if (selected) next.set(entry.path, entry)
  else next.delete(entry.path)
  selectedEntries.value = next
  if (selected) selectionMode.value = true
}

function toggleSelectionMode() {
  selectionMode.value = !selectionMode.value
  if (!selectionMode.value) {
    selectedEntries.value = new Map()
  }
}

async function handleBatchDownload() {
  const paths = [...selectedEntries.value.values()].map(entry => entry.path).filter((value): value is string => !!value)
  if (paths.length === 0 || batchArchiveLoading.value) return
  batchArchiveLoading.value = true
  try {
    const response = await fetch(sdkApiUrl({
      url: '/bots/{bot_id}/container/fs/archive',
      path: { bot_id: props.botId },
    }), {
      method: 'POST',
      headers: {
        ...authHeaders(),
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({ paths }),
    })
    if (!response.ok) {
      throw new Error(await readErrorMessage(response, t('bots.files.downloadFailed')))
    }
    downloadBlob(await response.blob(), 'workspace-selection.tar.gz')
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.files.downloadFailed')))
  } finally {
    batchArchiveLoading.value = false
  }
}

function openBatchDeleteDialog() {
  if (!props.canWrite) return
  if (selectedCount.value === 0) return
  batchDeleteDialogOpen.value = true
}

async function handleBatchDelete() {
  if (!props.canWrite) return
  const targets = [...selectedEntries.value.values()]
  if (targets.length === 0 || batchDeleteLoading.value) return
  batchDeleteLoading.value = true
  let failed = 0
  try {
    for (const target of targets) {
      try {
        await postBotsByBotIdContainerFsDelete({
          path: { bot_id: props.botId },
          body: { path: target.path, recursive: target.isDir },
          throwOnError: true,
        })
      } catch {
        failed++
      }
    }
    batchDeleteDialogOpen.value = false
    selectedEntries.value = new Map()
    if (failed > 0) {
      toast.error(t('bots.files.batchDeletePartialFailed', { failed, total: targets.length }))
    } else {
      toast.success(t('bots.files.deleteSuccess'))
    }
    reloadAndBroadcast()
  } finally {
    batchDeleteLoading.value = false
  }
}

async function handleExtract(entry: HandlersFsFileInfo) {
  if (!props.canWrite) return
  if (!entry.path || extractLoading.value) return
  extractLoading.value = true
  try {
    const response = await fetch(sdkApiUrl({
      url: '/bots/{bot_id}/container/fs/extract',
      path: { bot_id: props.botId },
    }), {
      method: 'POST',
      headers: {
        ...authHeaders(),
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({ path: entry.path }),
    })
    if (!response.ok) {
      throw new Error(await readErrorMessage(response, t('bots.files.extractFailed')))
    }
    toast.success(t('bots.files.extractSuccess'))
    reloadAndBroadcast()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.files.extractFailed')))
  } finally {
    extractLoading.value = false
  }
}

function handleDownload(entry: HandlersFsFileInfo) {
  const url = sdkApiUrl({
    url: '/bots/{bot_id}/container/fs/download',
    path: { bot_id: props.botId },
    query: { path: entry.path ?? '', ...sdkAuthQuery() },
  })
  const a = document.createElement('a')
  a.href = url
  a.download = entry.isDir ? `${entry.name ?? 'folder'}.tar.gz` : (entry.name ?? 'file')
  a.click()
}

function requestNewFolder(entry: HandlersFsFileInfo) {
  if (entry.isDir && entry.path) openMkdirDialog(entry.path)
}

function requestUpload(entry: HandlersFsFileInfo) {
  if (entry.isDir && entry.path) triggerUpload(entry.path)
}

provide(FileTreeKey, {
  canWrite,
  selectionMode,
  refreshKey,
  revealPath,
  activePath,
  rootPath,
  listDirectory: safeListDirectory,
  isSelected,
  toggleSelect: toggleSelection,
  openFile: handleOpenFile,
  openFilePinned: handleOpenFilePinned,
  openFileToSide: handleOpenFileToSide,
  requestDownload: handleDownload,
  requestRename: openRenameDialog,
  requestDelete: openDeleteDialog,
  requestExtract: handleExtract,
  requestNewFolder,
  requestUpload,
})

// Reset + reload when the bot changes.
watch(() => props.botId, () => {
  selectedEntries.value = new Map()
  selectionMode.value = false
  revealPath.value = null
  reload()
}, { immediate: true })

// Auto-refresh listing when the chat agent runs a fs-mutating tool (write/edit/apply_patch/exec).
// Stay on the local reload() — calling reloadAndBroadcast here would re-bump
// fsChangedAt and the watcher would loop on itself.
watch(fsChangedAt, () => {
  if (!props.botId) return
  reload()
})

watch(() => [props.botId, props.active] as const, ([botId, active]) => {
  if (botId && active && isDocumentVisible()) {
    reload()
    startTreePoll()
  } else {
    stopTreePoll()
  }
}, { immediate: true })

onMounted(() => {
  document.addEventListener('visibilitychange', handleVisibilityChange)
  if (props.active && isDocumentVisible()) startTreePoll()
})

onBeforeUnmount(() => {
  document.removeEventListener('visibilitychange', handleVisibilityChange)
  stopTreePoll()
})

defineExpose({
  navigateTo,
  reload,
})
</script>
