<script setup lang="ts">
import { onMounted, computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronRight } from 'lucide-vue-next'
import { Spinner, TextButton } from '@felinic/ui'
import type { DirectoryListing, PickerDirectory } from './directory-picker-types'
import {
  treeAsideClass,
  treeGlyphSlotClass,
  treeIndentClass,
  treeRowClass,
  treeRowIdleClass,
  treeRowSelectedClass,
} from './tree-row'
import { useTreeDisclosure } from './tree-disclosure'
import { resolveApiErrorMessage } from '@/utils/api-error'

// One directory row in the folder picker. Mirrors file-tree-node's shape and
// disclosure behaviour, minus everything the Explorer needs and a picker does
// not (files, seti glyphs, context menus, multi-select).
//
// One deliberate difference from the Explorer: there, a row click toggles the
// folder. Here a row click SELECTS it — a picker where clicking your choice
// collapses it would be unusable — so expansion moves onto the chevron, and a
// click on a collapsed row opens it as well (select and drill in one motion,
// never closing what you just picked).

const props = defineProps<{
  /**
   * Absolute path of this directory. The root may pass '' to mean "the
   * target's default directory"; the listing then reports the real path.
   */
  path: string
  /** Row label. The root passes a surface name; children pass the directory name. */
  name: string
  depth: number
  selectedPath: string
  /** Lists child directories of a path. Rejects with a user-facing message. */
  listDirectory: (path: string) => Promise<DirectoryListing>
  expandOnMount?: boolean
}>()

const emit = defineEmits<{ select: [path: string] }>()

const { t } = useI18n()

const failure = ref('')
const children = ref<PickerDirectory[]>([])
// Paths come from the server already joined for the target's OS, so the node
// never builds one; the root learns its own path from its first listing.
const resolvedPath = ref(props.path)

const selected = computed(() => !!resolvedPath.value && props.selectedPath === resolvedPath.value)

const { expanded, loaded, spinnerVisible, expand, toggle, reload } = useTreeDisclosure(async () => {
  try {
    const listing = await props.listDirectory(resolvedPath.value)
    resolvedPath.value = listing.path
    children.value = listing.directories
    failure.value = ''
    return true
  } catch (error) {
    // The message is on the retry line below the row; a toast per node would
    // stack one per expanded folder. `failure` clears only on success so the
    // line holds its place during a retry instead of flickering out and back.
    failure.value = resolveApiErrorMessage(error, t('bots.folders.form.browseFailed'))
    return false
  }
})

function onRowClick() {
  // An unresolved root has no path to offer yet; the click still opens it.
  if (resolvedPath.value) emit('select', resolvedPath.value)
  if (!expanded.value) void expand()
}

onMounted(() => {
  if (props.expandOnMount) void expand()
})
</script>

<template>
  <div
    :class="[treeRowClass, selected ? treeRowSelectedClass : treeRowIdleClass]"
    role="button"
    tabindex="0"
    @click="onRowClick"
    @keydown.enter.exact.prevent="onRowClick"
    @keydown.space.prevent="onRowClick"
  >
    <span
      v-for="g in depth"
      :key="g"
      :class="treeIndentClass"
    />
    <span
      :class="treeGlyphSlotClass"
      @click.stop="toggle"
    >
      <Spinner
        v-if="spinnerVisible"
        class="text-muted-foreground"
      />
      <ChevronRight
        v-else
        :stroke-width="1.53"
        class="size-4 text-muted-foreground transition-[rotate]"
        :class="{ 'rotate-90': expanded && (loaded || !!failure) }"
      />
    </span>
    <span class="ml-1 min-w-0 flex-1 truncate">{{ name }}</span>
  </div>

  <template v-if="expanded">
    <div
      v-if="failure"
      :class="treeAsideClass"
    >
      <span
        v-for="g in depth + 1"
        :key="g"
        :class="treeIndentClass"
      />
      <span class="ml-1 min-w-0 flex-1 truncate text-destructive">{{ failure }}</span>
      <TextButton
        class="ml-2 shrink-0"
        @click.stop="reload"
      >
        {{ t('bots.folders.form.browseRetry') }}
      </TextButton>
    </div>

    <div
      v-else-if="loaded && !children.length"
      :class="treeAsideClass"
    >
      <span
        v-for="g in depth + 1"
        :key="g"
        :class="treeIndentClass"
      />
      <span class="ml-1 min-w-0 flex-1 truncate">{{ t('bots.folders.form.browseEmpty') }}</span>
    </div>

    <DirectoryPickerNode
      v-for="child in children"
      v-else
      :key="child.path"
      :path="child.path"
      :name="child.name"
      :depth="depth + 1"
      :selected-path="selectedPath"
      :list-directory="listDirectory"
      @select="emit('select', $event)"
    />
  </template>
</template>
