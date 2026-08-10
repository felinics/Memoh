<template>
  <!-- Right actions hug their content at the strip's far right (the void grows to
       fill — and accept drops — between the "+" cluster and here). Only the
       "Open Preview to the Side" action lives here, and only when this group's
       active tab is a markdown/html file (VS Code's editor-title preview action). -->
  <div class="flex h-full items-center pr-2">
    <Button
      v-if="previewPath"
      variant="ghost"
      shape="circle"
      class="size-7 shrink-0 p-0 text-muted-foreground hover:text-foreground"
      :title="t('chat.openPreviewToSide')"
      :aria-label="t('chat.openPreviewToSide')"
      @click="openPreviewToSide"
    >
      <Columns2 class="size-3.5" />
    </Button>
    <!-- Projects rail toggle — the mirror of the left rail's toggle in the
         FIRST group's prefix actions: one button, permanently in the tab
         strip, flipping its icon by state. The panel therefore carries no
         collapse control of its own, exactly like the left sidebar. Only the
         last group renders it so a split layout does not show it twice. -->
    <Button
      v-if="isLastGroup"
      variant="ghost"
      size="icon-sm"
      shape="circle"
      :class="railToggleClass"
      :title="projectsPanelOpen ? t('projects.collapse') : t('projects.expand')"
      :aria-label="projectsPanelOpen ? t('projects.collapse') : t('projects.expand')"
      :aria-pressed="projectsPanelOpen"
      @click="projectsPanelOpen = !projectsPanelOpen"
    >
      <PanelRightClose
        v-if="projectsPanelOpen"
        :stroke-width="1.75"
        class="size-4"
      />
      <PanelRightOpen
        v-else
        :stroke-width="1.75"
        class="size-4"
      />
    </Button>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Columns2, PanelRightClose, PanelRightOpen } from 'lucide-vue-next'
import { Button } from '@felinic/ui'
import type { DockviewApi, DockviewGroupPanelApi, IDockviewGroupPanel } from 'dockview-vue'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { useChatStore } from '@/store/chat-list'
import { useProjectsStore } from '@/store/projects'
import { storeToRefs } from 'pinia'
import { isHtmlFile, isMarkdownFile } from '@/components/file-manager/utils'
import { hasBotPermission } from '@/utils/bot-permissions'

const props = defineProps<{
  params: {
    api: DockviewGroupPanelApi
    containerApi: DockviewApi
    group: IDockviewGroupPanel
  }
}>()

const { t } = useI18n()
const store = useWorkspaceTabsStore()
const chatStore = useChatStore()
const { currentBotId, bots } = storeToRefs(chatStore)
const { panelOpen: projectsPanelOpen } = storeToRefs(useProjectsStore())

// Same chrome as the first group's prefix cluster (sidebar toggle / nav
// arrows) so the strip reads as one row of controls.
const railToggleClass = 'size-7 shrink-0 p-0 text-muted-foreground hover:text-foreground' /* ui-allow-style */

const currentBot = computed(() =>
  bots.value.find(bot => bot.id === currentBotId.value) ?? null,
)
const canWorkspaceRead = computed(() =>
  hasBotPermission(currentBot.value?.current_user_permissions ?? [], 'workspace_read'),
)

// Track THIS group's active tab (not the globally active panel) so the preview
// action only appears in the header of the group currently showing a
// previewable file — correct for split layouts.
const activePanelId = ref<string | null>(props.params.group.activePanel?.id ?? null)
const activePanelSub = props.params.api.onDidActivePanelChange(() => {
  activePanelId.value = props.params.group.activePanel?.id ?? null
})

// Last group carries the Projects toggle (mirror of prefix-header-actions'
// first-group rule), so a split layout renders exactly one.
const groups = props.params.containerApi.groups
const lastGroupId = ref(groups[groups.length - 1]?.id ?? '')
function refreshLastGroup() {
  const list = props.params.containerApi.groups
  lastGroupId.value = list[list.length - 1]?.id ?? ''
}
const groupSubs = [
  props.params.containerApi.onDidAddGroup(refreshLastGroup),
  props.params.containerApi.onDidRemoveGroup(refreshLastGroup),
]
const isLastGroup = computed(() => props.params.group.id === lastGroupId.value)

onBeforeUnmount(() => {
  activePanelSub.dispose()
  for (const sub of groupSubs) sub.dispose()
})

const previewPath = computed(() => {
  if (!canWorkspaceRead.value) return null
  const id = activePanelId.value
  if (!id || !id.startsWith('file:')) return null
  const path = id.slice('file:'.length)
  const name = path.slice(path.lastIndexOf('/') + 1)
  return isMarkdownFile(name) || isHtmlFile(name) ? path : null
})

function openPreviewToSide() {
  const path = previewPath.value
  if (!path) return
  const name = path.slice(path.lastIndexOf('/') + 1)
  store.openPreview(path, t('chat.previewTab', { name }), props.params.group.id)
}
</script>
