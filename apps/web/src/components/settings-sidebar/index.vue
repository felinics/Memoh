<template>
  <!-- Web: a plain resizable panel — width is driven by --sidebar-width (set
       from the dragged value, clamped to a readable minimum). Desktop pins a
       fixed 15rem. There is no icon-rail collapse; the minimum width is the
       floor, so the panel never shrinks into something cramped. -->
  <aside
    ref="asideEl"
    class="relative h-full"
    :style="{ '--sidebar-width': desktopShell ? '15rem' : `${sidebarWidth}px` }"
  >
    <Sidebar
      collapsible="none"
      :class="['workspace-divider-r', desktopShell && 'h-dvh']"
    >
      <div
        v-if="macTrafficReserve"
        class="h-12 shrink-0 [-webkit-app-region:drag]"
        aria-hidden="true"
      />
      <!-- Traffic-reserve top padding: clears the macOS traffic lights (bottom edge
           ≈28px from top) with a comfortable ~20px gap below them — tighter than the
           old full-width header's 62px (which read as over-reserved) but not cramped
           against the lights. Web has no traffic lights, so it keeps the bare 18px
           (which makes the back-row icon sit ~29px from top, matching its ~30px from
           the left — a balanced corner). -->
      <SidebarHeader
        v-if="!hideHeader"
        class="px-[16px] pb-3 border-0"
        :class="macTrafficReserve ? 'pt-0' : 'pt-[18px]'"
      >
        <NavItem
          @click="router.push(_backToChatRoute).catch(() => {})"
        >
          <ChevronLeft class="size-3.5 shrink-0" />
          <span>{{ t('sidebar.settings') }}</span>
        </NavItem>
      </SidebarHeader>

      <SidebarContent>
        <!-- Core group: no label -->
        <SidebarGroup class="px-[16px] pt-1 pb-0">
          <SidebarGroupContent>
            <SidebarMenu class="gap-1">
              <SidebarMenuItem
                v-for="item in coreNavItems"
                :key="item.name"
              >
                <NavItem
                  :active="isItemActive(item.name)"
                  :aria-current="isItemActive(item.name) ? 'page' : undefined"
                  @click="navigate(item.name)"
                >
                  <component
                    :is="item.icon"
                    :stroke-width="1.75"
                    class="size-4 shrink-0"
                    :class="item.flipX && '-scale-x-100'"
                  />
                  <span>{{ item.title }}</span>
                </NavItem>
              </SidebarMenuItem>
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>

        <!-- Integrations group -->
        <SidebarGroup
          v-if="integrationsNavItems.length"
          class="px-[16px] pt-4 pb-0"
        >
          <SidebarGroupLabel size="compact">
            {{ t('sidebar.group.integrations') }}
          </SidebarGroupLabel>
          <SidebarGroupContent class="pt-0">
            <SidebarMenu class="gap-1">
              <SidebarMenuItem
                v-for="item in integrationsNavItems"
                :key="item.name"
              >
                <NavItem
                  :active="isItemActive(item.name)"
                  :aria-current="isItemActive(item.name) ? 'page' : undefined"
                  @click="navigate(item.name)"
                >
                  <component
                    :is="item.icon"
                    :stroke-width="1.75"
                    class="size-4 shrink-0"
                    :class="item.flipX && '-scale-x-100'"
                  />
                  <span>{{ item.title }}</span>
                </NavItem>
              </SidebarMenuItem>
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>

        <!-- Account group -->
        <SidebarGroup
          v-if="accountNavItems.length"
          class="px-[16px] pt-4 pb-0"
        >
          <SidebarGroupLabel size="compact">
            {{ t('sidebar.group.account') }}
          </SidebarGroupLabel>
          <SidebarGroupContent class="pt-0">
            <SidebarMenu class="gap-1">
              <SidebarMenuItem
                v-for="item in accountNavItems"
                :key="item.name"
              >
                <NavItem
                  :active="isItemActive(item.name)"
                  :aria-current="isItemActive(item.name) ? 'page' : undefined"
                  @click="navigate(item.name)"
                >
                  <component
                    :is="item.icon"
                    :stroke-width="1.75"
                    class="size-4 shrink-0"
                    :class="item.flipX && '-scale-x-100'"
                  />
                  <span>{{ item.title }}</span>
                </NavItem>
              </SidebarMenuItem>
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>

      <!-- Width resize handle (web only). Drag to resize the sidebar; the width
           is clamped to [MIN_FULL, MAX_WIDTH] so it can't shrink past a readable
           minimum. Sits on the sidebar's right edge. -->
      <div
        v-if="!desktopShell"
        class="group/resize absolute right-0 top-0 z-(--z-sticky) h-full w-1 cursor-col-resize"
        @mousedown="onResizeStart"
      >
        <div
          class="h-full w-full transition-colors group-hover/resize:bg-border"
          :class="{ 'bg-ring': isResizing }"
        />
      </div>
    </Sidebar>
  </aside>
</template>

<script setup lang="ts">
import { computed, inject, onBeforeUnmount, ref, type Component } from 'vue'
import { useLocalStorage } from '@vueuse/core'
import { storeToRefs } from 'pinia'
import { useRouter, useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  AudioLines,
  Box,
  ChartNoAxesColumn,
  ChevronLeft,
  CircleUserRound,
  Database,
  Globe,
  Info,
  Keyboard,
  Laptop,
  Mail,
  MousePointer2,
  Store,
  Users,
  Video,
} from 'lucide-vue-next'
import AppearanceIcon from './appearance-icon.vue'
import { NavItem } from '@felinic/ui'
import { useChatSelectionStore } from '@/store/chat-selection'
import { useChatStore } from '@/store/chat-list'
import { useUserStore } from '@/store/user'
import {
  Sidebar,
  SidebarContent,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuItem,
} from '@felinic/ui'
import { DesktopShellKey } from '@/lib/desktop-shell'

const props = withDefaults(defineProps<{
  hideHeader?: boolean
  excludeItems?: string[]
  // When true, the sidebar reserves a draggable macOS traffic-light strip above
  // its visible controls.
  macTrafficReserve?: boolean
}>(), {
  hideHeader: false,
  excludeItems: () => [],
  macTrafficReserve: false,
})

defineEmits<{ back: [] }>()

const desktopShell = inject(DesktopShellKey, false)

// ---- resizable width (web only) ------------------------------------------
// The sidebar is a plain resizable panel: drag the right-edge handle to set its
// width, clamped to [MIN_FULL, MAX_WIDTH] so it can never shrink past a readable
// minimum. There is no icon-rail collapse — the minimum width is the floor.
// Desktop pins a fixed width and renders no handle, so this stays inert there.
const sidebarWidth = useLocalStorage('settings-sidebar-width', 240)

const MIN_FULL = 200
const MAX_WIDTH = 360

const asideEl = ref<HTMLElement | null>(null)
const isResizing = ref(false)

function clampWidth(w: number): number {
  return Math.min(MAX_WIDTH, Math.max(MIN_FULL, w))
}

function onResizeStart(e: MouseEvent): void {
  e.preventDefault()
  isResizing.value = true
  // Width is the pointer's distance from the sidebar's fixed left edge.
  const leftEdge = asideEl.value?.getBoundingClientRect().left ?? 0

  function onMouseMove(ev: MouseEvent): void {
    sidebarWidth.value = clampWidth(ev.clientX - leftEdge)
  }

  function onMouseUp(): void {
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

const router = useRouter()
const route = useRoute()
const { t } = useI18n()
const selectionStore = useChatSelectionStore()
const { currentBotId } = storeToRefs(selectionStore)
const chatStore = useChatStore()
const { bots } = storeToRefs(chatStore)
const userStore = useUserStore()
const { userInfo } = storeToRefs(userStore)

const _backToChatRoute = computed(() => {
  const botId = (currentBotId.value ?? '').trim()
  if (!botId) return { name: 'home' as const }
  const botName = bots.value.find((b) => b.id === botId)?.name ?? botId
  return { name: 'bot' as const, params: { botName } }
})

function navigate(name: string): void {
  router.push({ name } as Parameters<typeof router.push>[0]).catch(() => {})
}

function isItemActive(name: string): boolean {
  if (name === 'bots') {
    return route.path.startsWith('/settings/bots')
  }
  if (name === 'supermarket') {
    return route.path.startsWith('/settings/supermarket')
  }
  return route.name === name
}

type NavItem = { title: string; name: string; icon: Component; flipX?: boolean; adminOnly?: boolean }

function filterItems(items: NavItem[]): NavItem[] {
  return items.filter((item) => {
    if (item.adminOnly && userInfo.value.role !== 'admin') return false
    return props.excludeItems.length === 0 || !props.excludeItems.includes(item.name)
  })
}

const coreNavItems = computed<NavItem[]>(() => filterItems([
  { title: t('sidebar.bots'), name: 'bots', icon: MousePointer2, flipX: true },
  { title: t('sidebar.runtimes'), name: 'runtimes', icon: Laptop },
  { title: t('sidebar.providers'), name: 'providers', icon: Box },
  { title: t('sidebar.memory'), name: 'memory', icon: Database },
]))

const integrationsNavItems = computed<NavItem[]>(() => filterItems([
  { title: t('sidebar.webSearch'), name: 'web-search', icon: Globe },
  { title: t('sidebar.voice'), name: 'voice', icon: AudioLines },
  { title: t('sidebar.email'), name: 'email', icon: Mail },
  { title: t('sidebar.video'), name: 'video', icon: Video },
  { title: t('sidebar.supermarket'), name: 'supermarket', icon: Store },
  { title: t('sidebar.usage'), name: 'usage', icon: ChartNoAxesColumn },
  { title: t('sidebar.people'), name: 'people', icon: Users, adminOnly: true },
]))

const accountNavItems = computed<NavItem[]>(() => filterItems([
  { title: t('sidebar.appearance'), name: 'appearance', icon: AppearanceIcon },
  { title: t('sidebar.keyboard'), name: 'keyboard', icon: Keyboard },
  { title: t('sidebar.profile'), name: 'profile', icon: CircleUserRound },
  { title: t('sidebar.about'), name: 'about', icon: Info },
]))
</script>
