<script setup lang="ts">
import { onMounted, computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronRight } from 'lucide-vue-next'
import { Spinner, TextButton } from '@felinic/ui'
import { joinPath } from './utils'
import {
  treeAsideClass,
  treeGlyphSlotClass,
  treeIndentClass,
  treeRowClass,
  treeRowIdleClass,
  treeRowSelectedClass,
} from './tree-row'
import { useTreeDisclosure } from './tree-disclosure'

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
  path: string
  /** Row label. The root passes a surface name; children pass the directory name. */
  name: string
  depth: number
  selectedPath: string
  /** Lists child directory NAMES of a path. Rejects on failure. */
  listDirectory: (path: string) => Promise<string[]>
  expandOnMount?: boolean
}>()

const emit = defineEmits<{ select: [path: string] }>()

const { t } = useI18n()

const failed = ref(false)
const children = ref<string[]>([])

const selected = computed(() => props.selectedPath === props.path)

const { expanded, loaded, spinnerVisible, expand, toggle, reload } = useTreeDisclosure(async () => {
  try {
    children.value = await props.listDirectory(props.path)
    failed.value = false
    return true
  } catch {
    // The message is on the retry line below the row; a toast per node would
    // stack one per expanded folder. `failed` clears only on success so the
    // line holds its place during a retry instead of flickering out and back.
    failed.value = true
    return false
  }
})

function onRowClick() {
  emit('select', props.path)
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
    @keydown.enter.prevent="onRowClick"
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
        :class="{ 'rotate-90': expanded && (loaded || failed) }"
      />
    </span>
    <span class="ml-1 min-w-0 flex-1 truncate">{{ name }}</span>
  </div>

  <template v-if="expanded">
    <div
      v-if="failed"
      :class="treeAsideClass"
    >
      <span
        v-for="g in depth + 1"
        :key="g"
        :class="treeIndentClass"
      />
      <span class="ml-1 min-w-0 flex-1 truncate">{{ t('bots.folders.form.browseFailed') }}</span>
      <TextButton
        class="ml-2 shrink-0"
        @click.stop="reload"
      >
        {{ t('bots.folders.form.browseRetry') }}
      </TextButton>
    </div>

    <DirectoryPickerNode
      v-for="child in children"
      v-else
      :key="child"
      :path="joinPath(path, child)"
      :name="child"
      :depth="depth + 1"
      :selected-path="selectedPath"
      :list-directory="listDirectory"
      @select="emit('select', $event)"
    />
  </template>
</template>
