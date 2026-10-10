<template>
  <!-- PUSH/PULL RAIL. The rail is in flow (a flex sibling of the dock). It keeps
       a fixed width and slides out to the LEFT via margin-left (= -width when
       closed), so its flex footprint shrinks to 0 and the dock grows to fill the
       space — the content shifts rather than being covered. Only margin-left is
       transitioned, so the resize handle still tracks the pointer 1:1. `inert`
       while closed so focus can't tab into the parked-off-screen rail. -->
  <aside
    class="workspace-divider-r relative flex h-full min-h-0 shrink-0 flex-col overflow-hidden bg-sidebar"
    data-native-sidebar-surface
    data-native-sidebar-tint
    data-native-sidebar-seam
    :style="asideStyle"
    :inert="!workbenchOpen || undefined"
  >
    <!-- Workspace / bot switcher (no bottom divider — header blends into the
         panel below). Taller than the nav row and with its own vertical padding so
         the switcher chip/bar floats clear of the window's top edge instead of its
         hover fill kissing it. mac reserves the traffic-light gutter;
         web/Windows start at the normal pl-3 indent and the switcher goes
         full-width (right edge aligns with the search row below). -->
    <header
      class="flex h-11 shrink-0 items-center bg-sidebar pr-2 [-webkit-app-region:drag]"
      data-native-sidebar-surface
      :class="macTrafficReserve ? 'pl-22' : 'pl-3'"
    >
      <div class="min-w-0 flex-1">
        <BotSwitcher :full-width="!macTrafficReserve" />
      </div>
    </header>

    <!-- Horizontal nav + search: the active tab is a pill with
         icon + label, the others collapse to icon-only. These tabs are plain
         <button>s, NOT <Button>: the cva ships size paddings/gaps (and wraps the
         slot in a display:contents span) that fight the exact geometry we need.
         ANCHORED ON THE ICON: the icon never moves between states. Hovering an inactive tab shows a circle centered on the icon;
         activating it grows the pill to the RIGHT (label) AND a little to the
         LEFT, so the icon holds its exact screen position. The icon + label are
         ONE tight unit (gap = the label's pl, ~8px); "wider" is a more even
         envelope around that unit, never prying icon and text apart.

         HOW THE ICON STAYS PUT: icon-x = box-left + pl = anchor + ml + pl. The
         collapsed tab uses ml-0 / px-2 → a 32px circle with the icon
         centered. The active pill uses a larger SYMMETRIC px-2.5 for a
         flat envelope, plus a matching -ml-[3px] that bleeds the box
         left by exactly the extra left pad — so ml+pl stays stable and the icon
         doesn't budge. ml and pl animate on the SAME curve, so icon-x is constant
         across the whole tween: the pill visibly opens left+right around a still
         icon. (Inter-tab gap must exceed the 3px bleed so the second tab's
         leftward growth never overlaps the first — hence gap-1. The nav is also
         indented pl-3 so the active pill's 3px left bleed still clears the
         sidebar edge instead of kissing it.)

         GOTCHA (the ellipse bug): nothing on the GRID ITEM may carry padding — a
         border-box can't shrink below its own padding, so it would floor the 0fr
         track min width and break the circle. The icon→label gap lives on the
         INNER label span (clipped with the text when collapsed); the grid item
         is a bare overflow-hidden wrapper. -->
    <nav class="flex min-w-0 shrink-0 items-center gap-1 pl-3 pr-2 py-1.5">
      <TooltipProvider>
        <Tooltip
          v-for="view in availableViews"
          :key="view.id"
        >
          <TooltipTrigger as-child>
            <button
              type="button"
              class="inline-flex h-8 min-w-0 shrink-0 cursor-pointer items-center justify-start rounded-full px-2 text-muted-foreground outline-none transition-[margin,padding,color,background-color] duration-200 ease-[cubic-bezier(0.32,0.72,0,1)] hover:bg-[color:var(--sidebar-hover)] hover:text-foreground dark:hover:text-[color:oklch(0.96_0_0)] focus-visible:ring-2 focus-visible:ring-ring data-[active=true]:-ml-[3px] data-[active=true]:bg-sidebar-accent data-[active=true]:pl-2.5 data-[active=true]:pr-3.5 data-[active=true]:text-foreground/90 dark:data-[active=true]:text-[color:oklch(0.96_0_0)]"
              :data-active="sidebarView === view.id"
              :aria-label="view.label"
              :aria-pressed="sidebarView === view.id"
              @click="store.selectSidebarView(view.id)"
            >
              <span class="relative shrink-0">
                <component
                  :is="view.icon"
                  :stroke-width="1.75"
                  class="size-[18px] shrink-0"
                />
                <!-- Unsaved files live on the Files view, so a count here surfaces them
                     even while the user is in Chat. -->
                <BadgeCount
                  v-if="view.id === 'files' && dirtyFileCount > 0"
                  :count="dirtyFileCount"
                  class="pointer-events-none absolute -right-1.5 -top-1"
                />
              </span>
              <span
                class="grid min-w-0 transition-[grid-template-columns] duration-200 ease-[cubic-bezier(0.32,0.72,0,1)]"
                :class="sidebarView === view.id ? 'grid-cols-[1fr]' : 'grid-cols-[0fr]'"
              >
                <span class="min-w-0 overflow-hidden">
                  <span class="block whitespace-nowrap pl-2 text-control font-[550]">{{ view.label }}</span>
                </span>
              </span>
            </button>
          </TooltipTrigger>
          <TooltipContent side="bottom">
            {{ view.label }}
          </TooltipContent>
        </Tooltip>
      </TooltipProvider>

      <div class="flex-1" />

      <Button
        variant="ghost"
        size="icon-sm"
        class="shrink-0 rounded-full text-muted-foreground hover:text-foreground"
        :title="t('chat.searchSessions')"
        :aria-label="t('chat.searchSessions')"
        @click="searchOpen = true"
      >
        <Search
          :stroke-width="2"
          class="size-[18px]"
        />
      </Button>
    </nav>

    <!-- Active view (mutually exclusive). A bottom fade dissolves the list into
         the footer so the account row reads as floating just below it — instead
         of a hard rule above it, which would be lopsided since the nav above
         the list has no divider of its own. -->
    <div class="relative min-h-0 flex-1 overflow-hidden">
      <PanelSessions
        v-show="sidebarView === 'sessions'"
        class="h-full"
      />
      <PanelFiles
        v-if="canWorkspaceRead"
        v-show="sidebarView === 'files'"
        class="h-full"
      />
      <PanelSchedule
        v-show="sidebarView === 'schedule'"
        class="h-full"
      />
      <PanelSupermarket
        v-if="supermarketMounted"
        v-show="sidebarView === 'supermarket'"
        :bot-id="currentBotId || ''"
        :can-manage="hasBotPermission(currentBot?.current_user_permissions, 'manage')"
        class="h-full"
      />
      <div
        class="pointer-events-none absolute inset-x-0 bottom-0 h-6 bg-gradient-to-t from-sidebar to-transparent"
        data-native-sidebar-fade
      />
    </div>

    <!-- Usage-example entry on the sidebar floor, directly above the account
         row, so it stays reachable whichever panel is active. Same solid
         surface as the footer so list rows fade out behind it. -->
    <div
      class="relative z-1 shrink-0 bg-sidebar px-2 pt-1"
      data-native-sidebar-surface
    >
      <SidebarExamplesEntry />
    </div>

    <!-- Footer: account menu + update chip, pinned below the scrollable panel.
         The user block is min-w-0/flex-1 so the chip's hover expansion eats its
         slack instead of overlapping it. px-2.5/pb-2.5 keep the row's hover
         chip equidistant from the window's left and bottom edges — a notch
         wider than the panel's px-2 column above, deliberately. Solid bg +
         z-index keep list rows behind the footer on Web; the native-surface
         hook lets macOS Desktop expose the same sidebar material as the
         surrounding rail. -->
    <div
      class="relative z-1 flex shrink-0 items-center gap-2.5 bg-sidebar px-2.5 pt-1 pb-2.5"
      data-native-sidebar-surface
    >
      <!-- DropdownMenu has no DOM root; the flex sizing belongs on a real wrapper. -->
      <div class="min-w-0 flex-1">
        <UserMenu />
      </div>
      <UpdateChip />
    </div>

    <!-- Width resize handle -->
    <div
      class="group absolute right-0 top-0 z-(--z-raised) h-full w-1 cursor-col-resize"
      @mousedown="onResizeStart"
    >
      <div
        class="h-full w-full transition-colors group-hover:bg-border"
        :class="{ 'bg-ring': isResizing }"
      />
    </div>

    <SessionSearchDialog v-model:open="searchOpen" />
  </aside>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch, type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import { storeToRefs } from 'pinia'
import { Files, MessageCircle, Search, Calendar, Blocks } from 'lucide-vue-next'
import { BadgeCount, Button, Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@felinic/ui'
import { useSettingsStore } from '@/store/settings'
import { useChatStore } from '@/store/chat-list'
import { useWorkspaceTabsStore, type SidebarView } from '@/store/workspace-tabs'
import { hasBotPermission } from '@/utils/bot-permissions'
import BotSwitcher from './bot-switcher.vue'
import SidebarExamplesEntry from './examples-entry.vue'
import UserMenu from './user-menu.vue'
import UpdateChip from './update-chip.vue'
import PanelSessions from './panel-sessions.vue'
import PanelFiles from './panel-files.vue'
import PanelSchedule from './panel-schedule.vue'
import PanelSupermarket from './panel-supermarket.vue'
import SessionSearchDialog from './session-search-dialog.vue'

defineProps<{
  macTrafficReserve?: boolean
}>()

interface ActivityView {
  id: SidebarView
  label: string
  icon: Component
}

const { t, locale } = useI18n()
const store = useWorkspaceTabsStore()
const { sidebarView, sidebarWidth, workbenchOpen, dirtyFileCount } = storeToRefs(store)
const settingsStore = useSettingsStore()
const chatStore = useChatStore()
const { currentBotId, bots } = storeToRefs(chatStore)

// 按当前语言最长的导航标签保留最小空间，避免所有语言都为日文标签预留宽度。
// 同一语言下切换选项不改变下限；放大界面字号时同步扩大。
const minWidth = computed(() => {
  const baseWidth = locale.value === 'ja' ? 344 : locale.value === 'zh' ? 256 : 304
  return Math.ceil(baseWidth * Math.max(1, settingsStore.uiFontSizePx / 16))
})
const MAX_WIDTH = 480
// 旧的持久化宽度也须遵守新下限，收起位移和拖拽起点使用同一实际宽度。
const effectiveWidth = computed(() => Math.min(MAX_WIDTH, Math.max(minWidth.value, sidebarWidth.value)))

// Push/pull rail. Fixed WIDTH (driven 1:1 by the resize handle), and a
// margin-left that parks it off-screen left when closed so its flex footprint
// goes to 0 and the dock fills the gap. ONLY margin-left transitions — width
// stays untransitioned so a resize tracks the pointer exactly; and margin-left
// never changes during a resize (it's 0 while open), so the resize is clean.
const asideStyle = computed<Record<string, string>>(() => ({
  width: `${effectiveWidth.value}px`,
  marginLeft: workbenchOpen.value ? '0px' : `-${effectiveWidth.value}px`,
  transition: 'margin-left 300ms cubic-bezier(0.32, 0.72, 0, 1)',
  // Sidebar-scoped: lighten EVERY ghost button's hover (New Session, Settings,
  // Search) to the subtle sidebar tint so nothing on the rail uses the heavy
  // control-tier gray. The teleported bot dropdown lives outside this subtree
  // and keeps its own menu tokens.
  '--btn-ghost-hover': 'var(--sidebar-hover)',
}))

const searchOpen = ref(false)
const supermarketMounted = ref(false)
/** Keep installation dialogs alive when switching sidebar views after the first visit. */
watch(sidebarView, (view) => {
  if (view === 'supermarket') supermarketMounted.value = true
}, { immediate: true })

const currentBot = computed(() =>
  bots.value.find(bot => bot.id === currentBotId.value) ?? null,
)
const canWorkspaceRead = computed(() =>
  hasBotPermission(currentBot.value?.current_user_permissions, 'workspace_read'),
)

const availableViews = computed<ActivityView[]>(() => {
  const views: ActivityView[] = [
    { id: 'sessions', label: t('chat.activityBar.sessions'), icon: MessageCircle },
  ]
  if (canWorkspaceRead.value) {
    views.push({ id: 'files', label: t('chat.activityBar.files'), icon: Files })
  }
  views.push({ id: 'schedule', label: t('chat.activityBar.schedule'), icon: Calendar })
  views.push({ id: 'supermarket', label: t('supermarket.title'), icon: Blocks })
  return views
})

// If the persisted view becomes unavailable (e.g. permission lost), fall back.
watch(availableViews, (views) => {
  if (!views.some(view => view.id === sidebarView.value)) {
    sidebarView.value = 'sessions'
  }
}, { immediate: true })

const isResizing = ref(false)

function onResizeStart(e: MouseEvent) {
  e.preventDefault()
  isResizing.value = true
  const startX = e.clientX
  const startWidth = effectiveWidth.value

  function onMouseMove(ev: MouseEvent) {
    const delta = ev.clientX - startX
    sidebarWidth.value = Math.min(MAX_WIDTH, Math.max(minWidth.value, startWidth + delta))
  }

  function onMouseUp() {
    isResizing.value = false
    document.removeEventListener('mousemove', onMouseMove)
    document.removeEventListener('mouseup', onMouseUp)
    document.body.style.cursor = ''
    document.body.style.userSelect = ''
  }

  document.body.style.cursor = 'col-resize'
  document.body.style.userSelect = 'none'
  document.addEventListener('mousemove', onMouseMove)
  document.addEventListener('mouseup', onMouseUp)
}

onBeforeUnmount(() => {
  document.body.style.cursor = ''
  document.body.style.userSelect = ''
})
</script>
