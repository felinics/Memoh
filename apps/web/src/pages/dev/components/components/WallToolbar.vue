<script setup lang="ts">
// Wall toolbar: theme + color-scheme switching (reusing the app's settings
// store, zero new state). Flipping either restyles every specimen and swatch
// live.
import { storeToRefs } from 'pinia'
import { Moon, Sun, Palette } from 'lucide-vue-next'
import {
  Button,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@felinic/ui'
import { useSettingsStore } from '@/store/settings'
import { colorSchemes, type ColorSchemeId } from '@/constants/color-schemes'
import { useMacTrafficReserve } from '@/composables/useMacTrafficReserve'

const settings = useSettingsStore()
const { theme, colorScheme } = storeToRefs(settings)
const { setTheme, setColorScheme } = settings

// In the Electron desktop shell the titlebar is hidden, so this toolbar is the
// only chrome at the top of the window. Make its empty space a drag handle and
// clear the macOS traffic lights so they don't overlap the brand label.
const macTopInset = useMacTrafficReserve()
</script>

<template>
  <header
    class="sticky top-0 z-(--z-panel) flex flex-wrap items-center gap-3 border-b border-border bg-background/90 px-4 py-2.5 backdrop-blur [-webkit-app-region:drag]"
    :class="macTopInset ? 'pl-[84px]' : ''"
  >
    <div class="flex items-center gap-1.5">
      <Palette class="size-4 text-brand" />
      <span class="text-sm font-semibold">Component Wall</span>
      <span class="rounded bg-muted px-1.5 py-0.5 text-[10px] font-mono text-muted-foreground">dev</span>
    </div>

    <div class="ml-auto flex flex-wrap items-center gap-2 [-webkit-app-region:no-drag]">
      <!-- Theme -->
      <div class="flex items-center gap-1 rounded-lg border border-border p-0.5">
        <Button
          variant="ghost"
          size="icon-sm"
          :class="theme === 'light' ? 'bg-accent text-foreground' : ''"
          aria-label="Light theme"
          @click="setTheme('light')"
        >
          <Sun class="size-4" />
        </Button>
        <Button
          variant="ghost"
          size="icon-sm"
          :class="theme === 'dark' ? 'bg-accent text-foreground' : ''"
          aria-label="Dark theme"
          @click="setTheme('dark')"
        >
          <Moon class="size-4" />
        </Button>
      </div>

      <!-- Color scheme -->
      <Select
        :model-value="colorScheme"
        @update:model-value="(v) => v && setColorScheme(v as ColorSchemeId)"
      >
        <SelectTrigger class="w-40">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem
            v-for="scheme in colorSchemes"
            :key="scheme.id"
            :value="scheme.id"
          >
            <span class="flex items-center gap-2">
              <span class="flex gap-0.5">
                <span
                  v-for="sw in scheme.swatches"
                  :key="sw"
                  class="size-2.5 rounded-full border border-border"
                  :style="{ backgroundColor: sw }"
                />
              </span>
              <span class="capitalize">{{ scheme.id }}</span>
            </span>
          </SelectItem>
        </SelectContent>
      </Select>
    </div>
  </header>
</template>
