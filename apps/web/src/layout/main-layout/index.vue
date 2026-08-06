<template>
  <section class="flex w-full h-full overflow-hidden">
    <sidebar-provider
      v-model:open="isOpen"
      class="min-h-0 h-full"
      :default-open="sidebarDefaultOpen"
      disable-default-shortcut
    >
      <section class="relative">
        <slot name="sidebar" />
      </section>

      <section class="main-left-section" />
      <slot name="main" />
      <section class="main-right-section" />
    </sidebar-provider>
  </section>
</template>
<script setup lang="ts">
import { ref, inject } from 'vue'
import { SidebarProvider } from '@felinic/ui'
import { DesktopShellKey } from '@/lib/desktop-shell'

// In the desktop shell the sidebar collapse affordance is intentionally
// disabled — we keep the sidebar pinned open.
const desktopShell = inject(DesktopShellKey, false)

const sidebarDefaultOpen = desktopShell || !document.cookie.includes('sidebar_state=false')
const isOpen = ref(sidebarDefaultOpen)
</script>
