<template>
  <!-- Mobile main navigation hosted in a left Sheet. The content and its
       select-to-dismiss behavior live in MobileNavPanel; the built-in corner
       close is hidden — dismissal is a selection, a scrim tap, or Esc. -->
  <Sheet
    :open="mobileNavOpen"
    @update:open="onSheetOpenChange"
  >
    <SheetContent
      side="left"
      class="safe-area-frame w-72 bg-sidebar text-sidebar-foreground [&>button]:hidden"
    >
      <SheetHeader class="sr-only">
        <SheetTitle>{{ t('chat.mobileNav.title') }}</SheetTitle>
        <SheetDescription>{{ t('chat.mobileNav.description') }}</SheetDescription>
      </SheetHeader>
      <MobileNavPanel />
    </SheetContent>
  </Sheet>
</template>

<script setup lang="ts">
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@felinic/ui'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import MobileNavPanel from './mobile-nav-panel.vue'

const { t } = useI18n()
const workspaceTabs = useWorkspaceTabsStore()
const { mobileNavOpen } = storeToRefs(workspaceTabs)

function onSheetOpenChange(open: boolean) {
  if (open) workspaceTabs.openMobileNav()
  else workspaceTabs.closeMobileNav()
}
</script>
