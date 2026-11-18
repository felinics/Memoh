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
      @slot-mounted="onSlotMounted"
      @slot-unmounted="onSlotUnmounted"
    />

    <!-- Empty workspace placeholder -->
    <div
      v-if="!hasAnyTabs"
      class="flex-1 bg-background"
    >
      <PanePlaceholder :title="t('chat.workspaceEmpty')">
        {{ t('chat.workspaceEmptyHint') }}
      </PanePlaceholder>
    </div>

    <!-- Single flat keep-alive panel stage -->
    <WorkbenchStage
      v-show="hasAnyTabs"
      :slot-map="slotMap"
      :slot-version="slotVersion"
    />

    <!-- Transparent pointer shield during tab drag or sash resize so iframes never steal events -->
    <div
      v-if="store.panelDragging"
      class="absolute inset-0 z-(--z-overlay)"
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
      class="pointer-events-none fixed z-(--z-top) max-w-52 truncate rounded-md border border-border bg-popover px-2.5 py-1 text-label text-popover-foreground shadow-[var(--shadow-md)]"
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
import { activeWorkbenchDrag } from '@/workbench/drag'
import WorkbenchTree from './workbench/workbench-tree.vue'
import WorkbenchStage from './workbench/workbench-stage.vue'

const { t } = useI18n()
const store = useWorkspaceTabsStore()
const { pendingClose } = storeToRefs(store)
const activeDrag = activeWorkbenchDrag

const rootEl = ref<HTMLElement | null>(null)
const slotMap = new Map<string, HTMLElement>()
const slotVersion = ref(0)
const savingClose = ref(false)

const hasAnyTabs = computed(() => Object.keys(store.workbench.tabs).length > 0)

function onSlotMounted(payload: { groupId: string, el: HTMLElement }) {
  slotMap.set(payload.groupId, payload.el)
  slotVersion.value += 1
}

function onSlotUnmounted(payload: { groupId: string, el: HTMLElement | null }) {
  const current = slotMap.get(payload.groupId)
  if (current && (!payload.el || current === payload.el)) {
    slotMap.delete(payload.groupId)
    slotVersion.value += 1
  }
}

const dropPreviewStyle = computed<Record<string, string> | null>(() => {
  const target = activeDrag.value?.target
  const root = rootEl.value
  if (!target || !root) return null
  const rr = root.getBoundingClientRect()
  const scaleX = root.clientWidth > 0 ? rr.width / root.clientWidth : 1
  const scaleY = root.clientHeight > 0 ? rr.height / root.clientHeight : 1
  const sx = scaleX > 0 ? scaleX : 1
  const sy = scaleY > 0 ? scaleY : 1
  return {
    left: `${(target.rect.left - rr.left) / sx}px`,
    top: `${(target.rect.top - rr.top) / sy}px`,
    width: `${target.rect.width / sx}px`,
    height: `${target.rect.height / sy}px`,
  }
})

const dragChipStyle = computed<Record<string, string> | null>(() => {
  const drag = activeDrag.value
  if (!drag) return null
  return {
    left: `${drag.clientX + 8}px`,
    top: `${drag.clientY + 8}px`,
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
  slotMap.clear()
  store.unmountWorkspace()
})
</script>
