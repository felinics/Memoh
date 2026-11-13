<template>
  <!-- ui-allow-z: tab-local paint order (shape → label → hover-fill → divider
       → dot/close), never compared against another component's z-index — see
       dockview-theme.css's ::before/::after for the other half of this stack. -->
  <div
    ref="rootEl"
    class="group/tab relative z-[1] flex h-full min-w-0 items-center overflow-visible pr-[var(--tab-close-reserve)] pb-[var(--tab-inset)] pl-[var(--tab-pad-inline)]"
    @auxclick.middle.prevent="close"
  >
    <svg
      v-if="isVisible"
      class="active-tab-shape z-0"
      :viewBox="activeTabViewBox"
      :style="activeTabShapeStyle"
      preserveAspectRatio="none"
      aria-hidden="true"
      focusable="false"
    >
      <defs>
        <linearGradient
          :id="activeTabStrokeGradientId"
          x1="0"
          y1="0"
          x2="1"
          y2="0"
        >
          <stop
            offset="0%"
            stop-color="var(--dock-stroke)"
          />
          <stop
            offset="5%"
            stop-color="var(--workspace-tab-outline-color)"
          />
          <stop
            offset="95%"
            stop-color="var(--workspace-tab-outline-color)"
          />
          <stop
            offset="100%"
            stop-color="var(--dock-stroke)"
          />
        </linearGradient>
      </defs>
      <path
        :d="activeTabFillPath"
        fill="var(--surface-editor)"
      />
      <path
        :d="activeTabStrokePath"
        fill="none"
        :stroke="`url(#${activeTabStrokeGradientId})`"
        :stroke-width="activeTabStrokeWidth"
      />
    </svg>
    <!-- ui-allow-z: same tab-local stack as the root div above — this label
         only needs to sit above the active-tab SVG fill, nothing global.
         Active state is signalled by text colour, fill, and the connected chip
         shape, not by weight or size. Every tab is the same height. Label and
         close share the strip's one optical centre (see the geometry contract in
         dockview-theme.css): the tab's top inset plus this bottom padding give
         both the same centred box, so neither needs a per-control nudge. -->
    <span
      class="relative z-[1] min-w-0 flex-1 truncate text-label leading-[1.3] tracking-normal transition-colors"
      :class="[
        isActive ? 'text-foreground' : 'text-muted-foreground',
      ]"
    >{{ title }}</span>
    <!-- ui-allow-z: same tab-local stack — this dot only orders against its own
         tab's SVG/label/hover-fill siblings.
         Unsaved-changes dot: sits in the close slot at rest so the affordance never
         shifts; hovering fades it out as the close button fades in. It borrows the
         close-fade GEOMETRY (identical top/bottom/right) so the dot sits exactly
         where the close button will appear — no positional jump on hover — but the
         `is-dot` modifier strips the fade's paint. The grey blot is a HOVER
         affordance (it dissolves the title under the close glyph); on an inactive
         tab --tab-hover-bg resolves to the hover overlay (--surface-chrome-hover),
         so painting it at rest smeared grey behind the dot on a tab that is neither
         active nor hovered. At rest we want the dot ALONE; the blot returns exactly
         when the close button does (this layer fades out as that one fades in). -->
    <div
      v-if="isDirty"
      class="close-fade is-dot pointer-events-none absolute right-[var(--tab-close-edge)] z-[2] flex items-center pl-6 pr-0 opacity-100 group-hover/tab:opacity-0"
    >
      <span class="flex size-5 items-center justify-center">
        <span
          class="size-[7px] rounded-full"
          :class="isActive ? 'bg-foreground' : 'bg-muted-foreground'"
        />
      </span>
    </div>
    <!-- ui-allow-z: same tab-local stack — this close affordance only orders
         against its own tab's SVG/label/hover-fill/dot siblings.
         Close affordance: RESIDENT on the active tab (fills the reserved slot so it
         never reads as an empty gap), hover/focus-only on inactive tabs. Absolutely
         positioned so it never reserves a slot or resizes the chip (geometry is
         identical hovered or not). It paints the chip's own OPAQUE hover colour
         (--tab-hover-bg) as a left→right fade, so the title dissolves into the chip
         and nothing stays legible under the button. The fade layer is click-through;
         only the button takes pointer events. Keyboard focus reveals it for a11y;
         middle-click closes without it. -->
    <div
      class="close-fade pointer-events-none absolute right-[var(--tab-close-edge)] z-[2] flex items-center pl-6 pr-0 group-hover/tab:opacity-100 focus-within:opacity-100"
      :class="showResidentClose ? 'opacity-100' : 'opacity-0'"
    >
      <!-- No own hover fill: the close affordance is read through the left→right
           fade (which already paints the chip's hover surface) plus the icon
           darkening on hover. A second darker square behind the glyph just
           double-stacks chrome, so the ghost hover background is suppressed. -->
      <Button
        variant="ghost"
        class="pointer-events-auto size-5 shrink-0 rounded-sm p-0 text-muted-foreground [--btn-ghost-hover:transparent] hover:text-foreground"
        :aria-label="t('chat.tabMenu.close')"
        @pointerdown.stop
        @mousedown.stop
        @click.stop.prevent="close"
      >
        <X class="size-3.5" />
      </Button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { X } from 'lucide-vue-next'
import { Button } from '@felinic/ui'
import type { DockviewApi, DockviewPanelApi } from 'dockview-vue'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'

// Custom default tab: replaces dockview's built-in tab (square icon-hover
// block) with a design-system label + a ghost close button on a fixed slot.
const props = defineProps<{
  params: {
    api: DockviewPanelApi
    containerApi: DockviewApi
    params: Record<string, unknown>
  }
}>()

const { t } = useI18n()
const workspaceTabs = useWorkspaceTabsStore()

const rootEl = ref<HTMLElement | null>(null)
const DEFAULT_WORKSPACE_TAB_STROKE_WIDTH = 1
const activeTabStrokeGradientId = `active-tab-stroke-${useId()}`
const panelId = props.params.api.id
const title = ref(props.params.api.title ?? '')
const isActive = ref(props.params.api.isActive)
// Per-group active: this tab is its group's visible panel (every group has one).
// Drives the active SHAPE (SVG), the resident close slot, and the seam/hover CSS
// via the .memoh-tab-active class toggled on the .dv-tab wrapper. isActive is
// layout-level (only the focused group's active tab) and is kept ONLY for label
// colour — the focused group's active tab reads brighter, inactive groups' active
// tabs stay dim. dockview's own .dv-active-tab is isActive-scoped, so it can't
// carry this per-group signal the shape needs.
const isVisible = ref(props.params.api.isVisible)
// First-paint placeholder ONLY — these mirror the CSS contract (200≈12.5rem tab,
// 35 = 40px strip − 5px inset, 8 = --tab-radius, workspace hairline stroke) so the active
// SVG has a sane shape for the one frame before onMounted measures the real DOM.
// updateActiveTabShape() overwrites all of it; do NOT treat these as a source of
// truth — the CSS tokens are. They exist because the path can't be empty pre-mount.
const initialTabShape = buildActiveTabShape({
  width: 200,
  height: 35,
  radius: 8,
  strokeWidth: DEFAULT_WORKSPACE_TAB_STROKE_WIDTH,
})
const activeTabStrokeWidth = ref(DEFAULT_WORKSPACE_TAB_STROKE_WIDTH)
const activeTabViewBox = ref(initialTabShape.viewBox)
const activeTabFillPath = ref(initialTabShape.fillPath)
const activeTabStrokePath = ref(initialTabShape.strokePath)
const activeTabShapeStyle = ref<Record<string, string>>({
  left: '-8px',
  top: '0px',
  width: '216px',
  height: '35px',
})
let resizeObserver: ResizeObserver | null = null
let observedTab: HTMLElement | null = null
let pendingShapeFrame = 0
// Unsaved-changes flag for file panels — read from the store's reactive map, so
// the dot, the sidebar badge and the close dialog never drift apart.
const isDirty = computed(() => !!workspaceTabs.fileDirty[panelId])
// The close slot is reserved on every tab (the right inset), so on an inactive tab
// at rest it just reads as empty space. The ACTIVE tab fills that slot with a
// resident X — its close affordance is always there, not hover-only, so the
// reserved gap never looks accidental. A dirty active tab keeps showing the unsaved
// dot at rest instead (the X still fades in on hover over the same slot), so the two
// signals never fight for the slot.
const showResidentClose = computed(() => isVisible.value && !isDirty.value)
// Ephemeral preview tabs still get replaced in place when another
// preview-eligible tab opens into the same group (see workspace-tabs store), but
// the state is no longer surfaced visually — there is no italic or other marker.

const disposables = [
  props.params.api.onDidTitleChange((event) => {
    title.value = event.title
  }),
  props.params.api.onDidActiveChange((event) => {
    isActive.value = event.isActive
  }),
  props.params.api.onDidVisibilityChange((event) => {
    isVisible.value = event.isVisible
    if (event.isVisible) scheduleActiveTabShapeUpdate()
  }),
  props.params.containerApi.onDidLayoutChange(() => {
    // Re-tag on every layout change, not just visibility flips. When a tab is
    // MOVED or SPLIT into another group, dockview reuses this component's tab
    // element inside a brand-new .dv-tab wrapper (tabs.js setContent) without
    // remounting the Vue instance; and if the tab was active in both the old and
    // new group, isVisible stays true→true so onDidVisibilityChange never fires.
    // Without this, the moved active tab would render its SVG shape (v-if still
    // true) on a wrapper the CSS reads as inactive — doubled hover/divider pixels.
    // Layout change covers move/split/reorder and re-resolves the current wrapper.
    syncGroupActiveClass()
    syncShapeListeners(isVisible.value)
    scheduleActiveTabShapeUpdate()
  }),
]

// The tab part is initialized before the panel's title is applied (dockview
// sets it right after init), so re-read once the addPanel call stack settled.
onMounted(() => {
  title.value = props.params.api.title ?? title.value
  isActive.value = props.params.api.isActive
  isVisible.value = props.params.api.isVisible
  nextTick(() => {
    // Set the group-active class only after the tab's .dv-tab ancestor exists;
    // closest() returns null before the wrapper is in the DOM.
    syncGroupActiveClass()
    syncShapeListeners(isVisible.value)
    if (isVisible.value) scheduleActiveTabShapeUpdate()
  })
})

// Route through the store guard: a dirty file opens the save-confirm dialog
// instead of closing straight away; clean tabs close immediately.
function close() {
  workspaceTabs.requestCloseTab(panelId)
}

onBeforeUnmount(() => {
  if (pendingShapeFrame) cancelAnimationFrame(pendingShapeFrame)
  syncShapeListeners(false)
  // Clear the imperatively-set class so it can't linger on a .dv-tab wrapper that
  // dockview later reuses for a different (e.g. terminal) tab component that never
  // sets it — otherwise a stale active style could survive the swap.
  rootEl.value?.closest('.dv-tab')?.classList.remove('memoh-tab-active')
  for (const d of disposables) d.dispose()
})

watch(isVisible, (active) => {
  syncGroupActiveClass()
  syncShapeListeners(active)
  if (active) nextTick(scheduleActiveTabShapeUpdate)
})

// Mirror isVisible onto the .dv-tab wrapper as a class the theme CSS can target.
// dockview's own .dv-active-tab is isActive-scoped (layout-level, one tab total),
// so it can't express "this tab is its group's active panel" — which is what the
// seam/hover/shape rules need, since every group's active tab wears the shape.
function syncGroupActiveClass() {
  const tab = rootEl.value?.closest<HTMLElement>('.dv-tab')
  if (tab) tab.classList.toggle('memoh-tab-active', isVisible.value)
}

function syncShapeListeners(active: boolean) {
  if (!active) {
    resizeObserver?.disconnect()
    resizeObserver = null
    observedTab = null
    window.removeEventListener('resize', scheduleActiveTabShapeUpdate)
    return
  }
  const root = rootEl.value
  if (!root) return

  if (!resizeObserver) {
    resizeObserver = new ResizeObserver(() => scheduleActiveTabShapeUpdate())
    resizeObserver.observe(root)
    window.addEventListener('resize', scheduleActiveTabShapeUpdate)
  }

  const tab = root.closest<HTMLElement>('.dv-tab')
  const nextTab = tab && tab !== root ? tab : null
  if (nextTab !== observedTab) {
    if (observedTab) resizeObserver.unobserve(observedTab)
    observedTab = nextTab
    if (observedTab) resizeObserver.observe(observedTab)
  }
}

function scheduleActiveTabShapeUpdate() {
  if (!isVisible.value || pendingShapeFrame) return

  pendingShapeFrame = requestAnimationFrame(() => {
    pendingShapeFrame = 0
    updateActiveTabShape()
  })
}

function updateActiveTabShape() {
  const root = rootEl.value
  const tab = root?.closest<HTMLElement>('.dv-tab')
  if (!root || !tab) return

  const rootRect = root.getBoundingClientRect()
  const tabRect = tab.getBoundingClientRect()
  if (tabRect.width <= 0 || tabRect.height <= 0) return

  // getBoundingClientRect includes ancestor transforms. The first-entry shell
  // scales from 1.15 to 1, so writing that visual size back as a local CSS size
  // permanently enlarged the SVG until a resize forced another measurement.
  // Normalize the relative rect back into the tab's untransformed layout space.
  const tabStyle = getComputedStyle(tab)
  const layoutWidth = readLayoutDimension(tabStyle.width, tab.offsetWidth)
  const layoutHeight = readLayoutDimension(tabStyle.height, tab.offsetHeight)
  const visualScaleX = readVisualScale(tabRect.width, layoutWidth)
  const visualScaleY = readVisualScale(tabRect.height, layoutHeight)
  const relativeLeft = (tabRect.left - rootRect.left) / visualScaleX
  const relativeTop = (tabRect.top - rootRect.top) / visualScaleY

  const pixelRatio = window.devicePixelRatio || 1
  const alignedLeft = snapToDevicePixel(relativeLeft, pixelRatio)
  const alignedTop = snapToDevicePixel(relativeTop, pixelRatio)
  const alignedRight = snapToDevicePixel(relativeLeft + layoutWidth, pixelRatio)
  const alignedBottom = snapToDevicePixel(relativeTop + layoutHeight, pixelRatio)
  const width = alignedRight - alignedLeft
  const height = alignedBottom - alignedTop
  const strokeWidth = readWorkspaceTabStrokeWidth(tabStyle)
  const radius = Math.min(
    snapToDevicePixel(readTabRadius(tabStyle), pixelRatio),
    Math.max(0, width / 2),
    Math.max(0, height),
  )

  const shape = buildActiveTabShape({
    width,
    height,
    radius,
    strokeWidth,
  })

  activeTabStrokeWidth.value = strokeWidth
  activeTabViewBox.value = shape.viewBox
  activeTabFillPath.value = shape.fillPath
  activeTabStrokePath.value = shape.strokePath
  activeTabShapeStyle.value = {
    left: `${formatPx(alignedLeft - radius)}`,
    top: `${formatPx(alignedTop)}`,
    width: `${formatPx(width + radius * 2)}`,
    height: `${formatPx(height)}`,
  }
}

function readWorkspaceTabStrokeWidth(tabStyle: CSSStyleDeclaration) {
  const width = Number.parseFloat(tabStyle.getPropertyValue('--workspace-tab-stroke-width'))
  return Number.isFinite(width) && width > 0 ? width : DEFAULT_WORKSPACE_TAB_STROKE_WIDTH
}

function buildActiveTabShape({
  width,
  height,
  radius,
  strokeWidth,
}: {
  width: number
  height: number
  radius: number
  strokeWidth: number
}) {
  const strokeAdjustment = strokeWidth * 0.5
  const extension = radius
  const left = -extension
  const right = width + extension
  const extendedBottom = height
  const tabLeft = strokeAdjustment
  const tabRight = width - strokeAdjustment
  const tabTop = strokeAdjustment
  const tabBottom = height - strokeAdjustment
  const topRadius = Math.max(0, radius - strokeAdjustment)
  const bottomRadius = Math.max(0, radius - strokeAdjustment)

  const outline = [
    `M ${fmt(left)} ${fmt(extendedBottom)}`,
    `L ${fmt(left)} ${fmt(tabBottom)}`,
    `L ${fmt(tabLeft - bottomRadius)} ${fmt(tabBottom)}`,
    `A ${fmt(bottomRadius)} ${fmt(bottomRadius)} 0 0 0 ${fmt(tabLeft)} ${fmt(tabBottom - bottomRadius)}`,
    `L ${fmt(tabLeft)} ${fmt(tabTop + topRadius)}`,
    `A ${fmt(topRadius)} ${fmt(topRadius)} 0 0 1 ${fmt(tabLeft + topRadius)} ${fmt(tabTop)}`,
    `L ${fmt(tabRight - topRadius)} ${fmt(tabTop)}`,
    `A ${fmt(topRadius)} ${fmt(topRadius)} 0 0 1 ${fmt(tabRight)} ${fmt(tabTop + topRadius)}`,
    `L ${fmt(tabRight)} ${fmt(tabBottom - bottomRadius)}`,
    `A ${fmt(bottomRadius)} ${fmt(bottomRadius)} 0 0 0 ${fmt(tabRight + bottomRadius)} ${fmt(tabBottom)}`,
    `L ${fmt(right)} ${fmt(tabBottom)}`,
    `L ${fmt(right)} ${fmt(extendedBottom)}`,
  ].join(' ')

  return {
    viewBox: `${fmt(left)} 0 ${fmt(width + extension * 2)} ${fmt(height)}`,
    fillPath: `${outline} L ${fmt(left)} ${fmt(extendedBottom)} Z`,
    strokePath: outline,
  }
}

function readTabRadius(tabStyle: CSSStyleDeclaration) {
  const radius = Number.parseFloat(tabStyle.borderTopLeftRadius)
  return Number.isFinite(radius) && radius > 0 ? radius : 8
}

function readLayoutDimension(value: string, fallback: number) {
  const dimension = Number.parseFloat(value)
  return Number.isFinite(dimension) && dimension > 0 ? dimension : fallback
}

function readVisualScale(visualDimension: number, layoutDimension: number) {
  const scale = visualDimension / layoutDimension
  return Number.isFinite(scale) && scale > 0 ? scale : 1
}

function snapToDevicePixel(value: number, scale: number) {
  return Math.round(value * scale) / scale
}

function formatPx(value: number) {
  return `${fmt(value)}px`
}

function fmt(value: number) {
  const normalized = Math.abs(value) < 0.0001 ? 0 : value
  return Number(normalized.toFixed(3)).toString()
}
</script>

<style scoped>
.active-tab-shape {
  position: absolute;
  overflow: visible;
  pointer-events: none;
}

/* Close-fade: a left→right blot that paints the chip's own surface so the title
 * dissolves under the close button and nothing stays legible beneath the glyph.
 * It is TWO stacked gradients on purpose: --tab-hover-bg is the surface the tab
 * wears (opaque --surface-editor when active, but the TRANSLUCENT hover overlay
 * otherwise), so painting it alone left the title readable on a hovered tab.
 * Layering an opaque --surface-chrome base UNDER it composites to exactly what the
 * tab shows — chrome+overlay on hover, plain editor when active — but never
 * see-through. Absolutely positioned, so it never reserves a slot or resizes the
 * chip. (Geometry — why it's a centred band, not the full box — is on the rule.) */
.close-fade {
  /* A band centred on the 20px lane — NOT the full tab box. On an active tab this
   * paints the editor fill, so a full-height band with a straight top edge would
   * spill editor colour ABOVE the rounded crown onto the chrome strip (the "close
   * fill overflows" bug). The tab box floats --tab-inset below the strip top but
   * its bottom meets the pane, so to stay centred on the lane the bottom inset is
   * the top inset + --tab-inset. top:--tab-inset drops the band clear of the crown
   * arc while still covering the label; bottom:2×--tab-inset keeps it centred. */
  top: var(--tab-inset);
  bottom: calc(var(--tab-inset) * 2);
  /* Instant hide when hover ends — avoids white/grey close-fade lingering on a tab
   * you just switched away from. Fade-in only while the tab group is hovered. */
  transition: none;
  background:
    linear-gradient(to right, transparent, var(--tab-hover-bg, var(--surface-editor)) 1rem),
    linear-gradient(to right, transparent, var(--surface-chrome) 1rem);
  /* Right corners follow the hover pill radius so the fade never squares off the
   * fill on an inactive hovered tab. */
  border-radius: 0 var(--tab-hover-radius) var(--tab-hover-radius) 0;
}

.group\/tab:hover .close-fade,
.group\/tab:focus-within .close-fade {
  transition: opacity var(--tab-hover-duration, 150ms) ease-out;
}

/* Dirty-dot slot: reuse close-fade for GEOMETRY ONLY, never its paint. The fade is
 * a hover affordance (blots the title under the close button); this layer is shown
 * only at rest (opacity-100, fading to 0 on hover), so painting the fade here put a
 * grey smear behind the dot on an inactive tab — off-hover, --tab-hover-bg resolves
 * to the hover overlay, not the tab's own fill. Higher specificity than .close-fade
 * so it wins regardless of source order. */
.close-fade.is-dot {
  background: none;
}
</style>
