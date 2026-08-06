<template>
  <!-- No vertical seam against the first tab: separation is the gap + the strip's
       own outline (the bottom hairline, plus the active tab's crown/foot stroke),
       not a divider between the nav cluster and the tabs. The right padding keeps the buttons
       off the first tab; the tab's own pl-4 keeps its title off the gap. -->
  <div
    v-if="isFirstGroup && !isMobile"
    class="flex h-full items-center gap-0.5 pr-0 [-webkit-app-region:drag] transition-[padding] duration-300 ease-[cubic-bezier(0.32,0.72,0,1)]"
    :class="shouldReserveTrafficLight ? 'pl-[88px]' : 'pl-2'"
  >
    <Button
      variant="ghost"
      size="icon-sm"
      shape="circle"
      class="size-7 text-muted-foreground hover:text-foreground [-webkit-app-region:no-drag]"
      :title="workbenchOpen ? t('chat.topBar.hideWorkbench') : t('chat.topBar.showWorkbench')"
      :aria-label="workbenchOpen ? t('chat.topBar.hideWorkbench') : t('chat.topBar.showWorkbench')"
      :aria-pressed="workbenchOpen"
      @click="workspaceTabs.toggleWorkbench()"
    >
      <PanelLeftClose
        v-if="workbenchOpen"
        :stroke-width="1.75"
        class="size-4"
      />
      <PanelLeftOpen
        v-else
        :stroke-width="1.75"
        class="size-4"
      />
    </Button>
    <Button
      variant="ghost"
      size="icon-sm"
      shape="circle"
      class="size-7 text-muted-foreground hover:text-foreground [-webkit-app-region:no-drag]"
      :title="t('chat.topBar.goBack')"
      :aria-label="t('chat.topBar.goBack')"
      @click="router.go(-1)"
    >
      <ChevronLeft
        :stroke-width="1.75"
        class="size-4"
      />
    </Button>
    <Button
      variant="ghost"
      size="icon-sm"
      shape="circle"
      class="size-7 text-muted-foreground hover:text-foreground [-webkit-app-region:no-drag]"
      :title="t('chat.topBar.goForward')"
      :aria-label="t('chat.topBar.goForward')"
      @click="router.go(1)"
    >
      <ChevronRight
        :stroke-width="1.75"
        class="size-4"
      />
    </Button>
  </div>
  <!-- Non-first group: empty (zero-width) -->
  <div v-else />
</template>

<script setup lang="ts">
import { computed, inject, onBeforeUnmount, ref } from 'vue'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ChevronLeft, ChevronRight, PanelLeftClose, PanelLeftOpen } from 'lucide-vue-next'
import { Button } from '@felinic/ui'
import type { DockviewApi, DockviewGroupPanelApi, IDockviewGroupPanel } from 'dockview-vue'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { DesktopShellKey } from '@/lib/desktop-shell'

const props = defineProps<{
  params: {
    api: DockviewGroupPanelApi
    containerApi: DockviewApi
    group: IDockviewGroupPanel
  }
}>()

const { t } = useI18n()
const router = useRouter()
const workspaceTabs = useWorkspaceTabsStore()
// The mobile shell replaces this whole cluster with its own top bar (the rail
// it toggles is not rendered there), so the cluster stays desktop-only even if
// the group header becomes visible.
const { workbenchOpen, isMobile } = storeToRefs(workspaceTabs)

// Determine if this is the first (leftmost/topmost) group
const firstGroupId = ref(props.params.containerApi.groups[0]?.id ?? '')

function refreshFirstGroup() {
  firstGroupId.value = props.params.containerApi.groups[0]?.id ?? ''
}

const disposables = [
  props.params.containerApi.onDidAddGroup(() => refreshFirstGroup()),
  props.params.containerApi.onDidRemoveGroup(() => refreshFirstGroup()),
]

onBeforeUnmount(() => {
  for (const d of disposables) d.dispose()
})

const isFirstGroup = computed(() => props.params.group.id === firstGroupId.value)

// macOS traffic light reserve (only when sidebar is closed on desktop mac)
const desktopShell = inject(DesktopShellKey, false)
const macTrafficReserve = computed(() =>
  desktopShell
  && typeof navigator !== 'undefined'
  && navigator.platform.toLowerCase().includes('mac'),
)
const shouldReserveTrafficLight = computed(() =>
  macTrafficReserve.value && !workbenchOpen.value,
)
</script>
