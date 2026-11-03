<template>
  <!-- Dev-only floating switcher, mounted outside the app tree (index.ts).
       Collapsed it is a thin tab on the right edge, mid-height, where it
       covers neither the top bar nor the composer. -->
  <div
    v-if="isMobile"
    data-mobile-direction-switcher
    class="fixed right-0 top-1/2 z-(--z-top) -translate-y-1/2"
  >
    <button
      v-if="!open"
      type="button"
      class="flex h-14 w-5 cursor-pointer items-center justify-center rounded-l-md bg-foreground text-background opacity-40"
      aria-label="Mobile direction"
      @click="open = true"
    >
      <Palette class="size-3" />
    </button>
    <div
      v-else
      class="mr-2 flex w-80 flex-col gap-3 rounded-menu-shell border border-border bg-popover p-3 text-popover-foreground shadow-(--shadow-dropdown)"
    >
      <div class="flex items-center justify-between">
        <span class="text-label font-medium">Mobile direction</span>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="Close"
          @click="open = false"
        >
          <X />
        </Button>
      </div>
      <div class="flex flex-col gap-1.5">
        <span class="text-body text-muted-foreground">Visual</span>
        <SegmentedControl
          v-model="direction"
          :items="directionItems"
          aria-label="Visual direction"
        />
      </div>
      <div class="flex flex-col gap-1.5">
        <span class="text-body text-muted-foreground">Press feel</span>
        <SegmentedControl
          v-model="feel"
          :items="feelItems"
          aria-label="Press feel"
        />
      </div>
      <div class="flex flex-col gap-1.5">
        <span class="text-body text-muted-foreground">Navigation</span>
        <SegmentedControl
          v-model="navForm"
          :items="navItems"
          aria-label="Navigation"
        />
      </div>
      <div class="flex flex-col gap-1.5">
        <span class="text-body text-muted-foreground">Sent bubble</span>
        <SegmentedControl
          v-model="bubbleMotion"
          :items="bubbleItems"
          aria-label="Sent bubble"
        />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { Palette, X } from 'lucide-vue-next'
import { Button, SegmentedControl } from '@felinic/ui'
import { useIsMobile } from '@/composables/useIsMobile'
import type { MobileNavMode } from '@/shells/mobile/nav-mode'
import { bubbleMotion, bubbleMotions, direction, feel, mobileDirections, navForm, navForms, pressFeels, type BubbleMotion, type MobileDirection, type PressFeel } from './state'

const isMobile = useIsMobile()
const open = ref(false)
const directionItems = mobileDirections.map(option => ({ ...option })) as { value: MobileDirection, label: string }[]
const feelItems = pressFeels.map(option => ({ ...option })) as { value: PressFeel, label: string }[]
const bubbleItems = bubbleMotions.map(option => ({ ...option })) as { value: BubbleMotion, label: string }[]
const navItems = navForms.map(option => ({ ...option })) as { value: MobileNavMode, label: string }[]
</script>
