<template>
  <div
    data-workbench-prefix-nav
    class="flex h-full shrink-0 items-center gap-0.5 pr-0 transition-[padding] duration-300 ease-[cubic-bezier(0.32,0.72,0,1)]"
    :class="[
      shouldReserveTrafficLight ? 'pl-22' : 'pl-2',
      store.panelDragging ? '[-webkit-app-region:no-drag]' : '[-webkit-app-region:drag]',
    ]"
  >
    <Button
      variant="ghost"
      tone="muted"
      size="icon-sm"
      shape="circle"
      class="size-7 [-webkit-app-region:no-drag]"
      :title="store.workbenchOpen ? t('chat.topBar.hideWorkbench') : t('chat.topBar.showWorkbench')"
      :aria-label="store.workbenchOpen ? t('chat.topBar.hideWorkbench') : t('chat.topBar.showWorkbench')"
      :aria-pressed="store.workbenchOpen"
      @click.stop="store.toggleWorkbench()"
    >
      <SidebarCloseIcon
        v-if="store.workbenchOpen"
        class="size-4"
      />
      <SidebarOpenIcon
        v-else
        class="size-4"
      />
    </Button>
    <Button
      variant="ghost"
      tone="muted"
      size="icon-sm"
      shape="circle"
      class="size-7 [-webkit-app-region:no-drag]"
      :title="t('chat.topBar.goBack')"
      :aria-label="t('chat.topBar.goBack')"
      @click.stop="router?.go(-1)"
    >
      <BackIcon class="size-4" />
    </Button>
    <Button
      variant="ghost"
      tone="muted"
      size="icon-sm"
      shape="circle"
      class="size-7 [-webkit-app-region:no-drag]"
      :title="t('chat.topBar.goForward')"
      :aria-label="t('chat.topBar.goForward')"
      @click.stop="router?.go(1)"
    >
      <ForwardIcon class="size-4" />
    </Button>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  BackIcon,
  ForwardIcon,
  SidebarCloseIcon,
  SidebarOpenIcon,
} from '@memohai/icon/ui'
import { Button } from '@felinic/ui'
import { useMacTrafficReserve } from '@/composables/useMacTrafficReserve'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'

const { t } = useI18n()
const router = useRouter() as ReturnType<typeof useRouter> | undefined
const store = useWorkspaceTabsStore()
const macTrafficReserve = useMacTrafficReserve()

const shouldReserveTrafficLight = computed(() => macTrafficReserve.value && !store.workbenchOpen)
</script>
