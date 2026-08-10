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

const expanded = ref(false)
const loaded = ref(false)
const loading = ref(false)
const failed = ref(false)
const children = ref<string[]>([])

const selected = computed(() => props.selectedPath === props.path)

async function loadChildren() {
  loading.value = true
  failed.value = false
  try {
    children.value = await props.listDirectory(props.path)
    loaded.value = true
  } catch {
    // The message is on the retry line below the row; a toast per node would
    // stack one per expanded folder.
    failed.value = true
  } finally {
    loading.value = false
  }
}

async function expand() {
  expanded.value = true
  if (!loaded.value) await loadChildren()
}

function onRowClick() {
  emit('select', props.path)
  if (!expanded.value) void expand()
}

function onChevronClick() {
  if (expanded.value) expanded.value = false
  else void expand()
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
      @click.stop="onChevronClick"
    >
      <ChevronRight
        :stroke-width="1.53"
        class="size-4 text-muted-foreground transition-transform"
        :class="{ 'rotate-90': expanded }"
      />
    </span>
    <span class="ml-1 min-w-0 flex-1 truncate">{{ name }}</span>
  </div>

  <template v-if="expanded">
    <div
      v-if="loading"
      :class="treeAsideClass"
    >
      <span
        v-for="g in depth + 1"
        :key="g"
        :class="treeIndentClass"
      />
      <span :class="treeGlyphSlotClass">
        <Spinner class="size-3.5" />
      </span>
    </div>

    <div
      v-else-if="failed"
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
        @click.stop="loadChildren"
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
