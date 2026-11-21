<template>
  <div
    ref="rootEl"
    class="workbench-root relative flex h-full min-w-0 flex-1 flex-col overflow-hidden bg-background"
    :data-workbench-dragging="store.panelDragging ? 'true' : undefined"
  >
    <!-- Desktop split tree & group tab bars -->
    <WorkbenchTree
      v-show="!store.isMobile && hasAnyTabs"
      :key="store.workbench.root.id"
      :node="store.workbench.root"
    />

    <!-- Empty workspace placeholder -->
    <div
      v-if="!hasAnyTabs"
      class="flex flex-1 flex-col bg-background"
    >
      <header
        v-if="!store.isMobile"
        data-workbench-empty-header
        class="workbench-header flex w-full shrink-0 items-center overflow-hidden"
        :class="store.panelDragging ? '[-webkit-app-region:no-drag]' : '[-webkit-app-region:drag]'"
      >
        <WorkbenchPrefixNav />
      </header>
      <div
        class="min-h-0 flex-1"
        :data-workbench-slot-group-id="store.workbench.root.kind === 'group' ? store.workbench.root.id : undefined"
        data-workbench-empty-slot="true"
      >
        <PanePlaceholder :title="t('chat.workspaceEmpty')">
          {{ t('chat.workspaceEmptyHint') }}
        </PanePlaceholder>
      </div>
    </div>

    <!-- Single flat keep-alive panel stage -->
    <WorkbenchStage v-show="hasAnyTabs" />

    <!-- Transparent pointer shield during tab drag or sash resize so iframes never steal events -->
    <div
      v-if="store.panelDragging"
      class="absolute inset-0 z-(--z-overlay)"
      @contextmenu.prevent
    />

    <!-- Drop zone preview & floating drag chip -->
    <div
      v-if="dropPreviewStyle"
      data-workbench-drop-preview
      class="pointer-events-none absolute z-(--z-overlay)"
      :class="activeDrag?.target?.isInsertionLine ? 'bg-(--wb-drop-line) rounded-full' : 'workbench-drop-preview'"
      :style="dropPreviewStyle"
    />
    <div
      v-if="dragChipStyle && activeDrag"
      data-workbench-drag-chip
      class="pointer-events-none fixed left-0 top-0 z-(--z-top) max-w-52 truncate rounded-md border border-border bg-popover px-2.5 py-1 text-label text-popover-foreground shadow-[var(--shadow-md)] will-change-transform"
      :style="dragChipStyle"
    >
      {{ activeDrag.title }}
    </div>

    <!-- Unsaved file close confirmation dialog -->
    <Dialog
      :open="!!pendingClose"
      @update:open="onConfirmOpenChange"
    >
      <DialogContent
        class="sm:max-w-105"
        @close-auto-focus="onCloseAutoFocus"
      >
        <DialogHeader>
          <DialogTitle>{{ t('chat.unsaved.title', { name: pendingClose?.title ?? '' }) }}</DialogTitle>
          <DialogDescription>{{ t('chat.unsaved.description') }}</DialogDescription>
        </DialogHeader>
        <DialogFooter class="mt-4">
          <Button
            variant="ghost"
            @click="resolveClose('cancel')"
          >
            {{ t('common.cancel') }}
          </Button>
          <Button
            variant="outline"
            @click="resolveClose('discard')"
          >
            {{ t('chat.unsaved.dontSave') }}
          </Button>
          <Button
            :disabled="savingClose"
            @click="onSaveAndClose"
          >
            {{ t('chat.unsaved.save') }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, provide, ref } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import {
  Button,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  PanePlaceholder,
} from '@felinic/ui'
import '@/styles/workbench.css'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { openAssetPreviewKey, openInFileManagerKey } from '../composables/useFileManagerProvider'
import { normalizeFileManagerPath } from './file-manager-path'
import { activeWorkbenchDrag, cancelActiveWorkbenchDrag, getElementScale } from '@/workbench/drag'
import WorkbenchPrefixNav from './workbench/workbench-prefix-nav.vue'
import WorkbenchTree from './workbench/workbench-tree.vue'
import WorkbenchStage from './workbench/workbench-stage.vue'

const { t } = useI18n()
const store = useWorkspaceTabsStore()
const { pendingClose } = storeToRefs(store)
const activeDrag = activeWorkbenchDrag

const rootEl = ref<HTMLElement | null>(null)
const savingClose = ref(false)

const hasAnyTabs = computed(() => Object.keys(store.workbench.tabs).length > 0)

const dropPreviewStyle = computed<Record<string, string> | null>(() => {
  const target = activeDrag.value?.target
  const root = rootEl.value
  if (!target || !root) return null
  const rr = root.getBoundingClientRect()
  // Unscale viewport coords in case an ancestor (e.g. MainSection entry transition) applies CSS scale.
  const { x: sx, y: sy } = getElementScale(root, rr)
  return {
    left: `${(target.rect.left - rr.left) / sx}px`,
    top: `${(target.rect.top - rr.top) / sy}px`,
    width: `${target.rect.width / sx}px`,
    height: `${target.rect.height / sy}px`,
  }
})

const dragChipStyle = computed<Record<string, string> | null>(() => {
  const drag = activeDrag.value
  if (!drag || drag.railTab) return null
  return {
    transform: `translate3d(${drag.clientX + 8}px, ${drag.clientY + 8}px, 0)`,
  }
})

function resolveClose(action: 'save' | 'discard' | 'cancel') {
  void store.resolvePendingClose(action)
}

async function onSaveAndClose() {
  savingClose.value = true
  try {
    await store.resolvePendingClose('save')
  } finally {
    savingClose.value = false
  }
}

function onConfirmOpenChange(open: boolean) {
  if (!open) resolveClose('cancel')
}

function onCloseAutoFocus(event: Event) {
  event.preventDefault()
  rootEl.value?.querySelector<HTMLElement>('[data-workbench-active-panel="true"]')?.focus()
}

provide(openInFileManagerKey, (path: string, isDir = false) => {
  const normalizedPath = normalizeFileManagerPath(path)
  if (isDir) {
    store.openFilesAt(normalizedPath)
  } else {
    store.openFilePinned(normalizedPath)
  }
})

provide(openAssetPreviewKey, args => store.openAsset(args))

onMounted(() => {
  store.mountWorkspace()
})

onBeforeUnmount(() => {
  cancelActiveWorkbenchDrag()
  store.unmountWorkspace()
})
</script>
