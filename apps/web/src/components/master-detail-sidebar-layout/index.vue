<template>
  <SidebarProvider
    class="min-h-[initial]! absolute inset-0 max-md:[&_[data-slot=sidebar]]:hidden"
    :default-open="true"
    disable-default-shortcut
  >
    <!-- Fixed width, never w-fit: the panel must not be sized by its content, or a
         long back label / name would stretch the whole sidebar. Web keeps
         responsive widths; desktop matches SettingsSidebar's fixed 15rem width. -->
    <Sidebar
      class="relative! **:[[role=navigation]]:relative! sidebar-container h-full! border-0! [&_[data-sidebar=sidebar]]:bg-transparent!"
      :class="desktopShell ? 'w-(--desktop-sidebar-width)!' : 'w-48! lg:w-52! xl:w-60!'"
    >
      <SidebarContent
        class="overflow-hidden h-full flex flex-col"
        :class="flush ? 'p-0' : 'p-2 pb-4 pt-4'"
      >
        <!-- Default: a nested card shell (box-in-box) for pages that sit to the
             right of the settings nav. flush: this layout IS the primary nav, so
             it goes edge-to-edge with a right divider, matching SettingsSidebar. -->
        <div
          class="flex-1 flex flex-col overflow-hidden min-h-0"
          :data-native-sidebar-surface="flush || undefined"
          :data-native-sidebar-tint="flush || undefined"
          :class="flush
            ? 'workspace-divider-r bg-sidebar'
            : 'border border-border-soft bg-muted/10 rounded-lg'"
        >
          <!-- Integrated Header (if provided) -->
          <div
            v-if="slots['sidebar-header']"
            class="shrink-0"
          >
            <slot name="sidebar-header" />
          </div>
          
          <!-- Content Group with ScrollArea -->
          <ScrollArea class="flex-1 min-h-0">
            <div class="p-2 flex flex-col gap-1">
              <slot name="sidebar-content" />
            </div>
          </ScrollArea>

          <!-- Integrated Footer (if provided) -->
          <SidebarFooter
            v-if="slots['sidebar-footer']"
            class="p-2 pt-0"
          >
            <slot name="sidebar-footer" />
          </SidebarFooter>
        </div>
      </SidebarContent>
    </Sidebar>

    <SidebarInset class="min-w-0 overflow-hidden">
      <section class="flex-1 min-w-0 relative min-h-0 overflow-hidden">
        <slot name="detail" />
      </section>

      <div class="absolute right-4 top-0 h-10 z-(--z-sticky) md:hidden flex items-center">
        <Menu
          class="cursor-pointer p-2 size-9"
          @click="mobileOpen = !mobileOpen"
        />
      </div>

      <Sheet
        :open="mobileOpen"
        @update:open="(v: boolean) => mobileOpen = v"
      >
        <SheetContent
          data-sidebar="sidebar"
          side="left"
          class="bg-sidebar text-sidebar-foreground w-72 p-0 [&>button]:hidden"
        >
          <SheetHeader class="sr-only">
            <SheetTitle>Sidebar</SheetTitle>
            <SheetDescription>Sidebar navigation</SheetDescription>
          </SheetHeader>
          <div class="flex h-full w-full flex-col">
            <component
              :is="flush ? 'div' : SidebarHeader"
              class="shrink-0"
            >
              <slot name="sidebar-header" />
            </component>
            <SidebarContent class="px-2 scrollbar-none">
              <slot name="sidebar-content" />
            </SidebarContent>
            <SidebarFooter v-if="$slots['sidebar-footer']">
              <slot name="sidebar-footer" />
            </SidebarFooter>
          </div>
        </SheetContent>
      </Sheet>
    </SidebarInset>
  </SidebarProvider>
</template>

<script setup lang="ts">
import { Menu } from 'lucide-vue-next'
import { inject, ref, useSlots } from 'vue'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
  SidebarContent,
  SidebarFooter,
  SidebarHeader,
  SidebarProvider,
  Sidebar,
  SidebarInset,
  ScrollArea
} from '@felinic/ui'
import { DesktopShellKey } from '@/lib/desktop-shell'

withDefaults(defineProps<{
  // When true, this layout acts as the primary (only) sidebar: it drops the
  // nested-card chrome and sits flush against the viewport edge. Used by the
  // de-nested bot detail page; left false everywhere it nests under another nav.
  flush?: boolean
}>(), {
  flush: false,
})

const slots=useSlots()
const desktopShell = inject(DesktopShellKey, false)

const mobileOpen = ref(false)
</script>
