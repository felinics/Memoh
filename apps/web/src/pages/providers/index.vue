<script setup lang="ts">
// Providers settings — ONE page owning the whole provider family. The sidebar
// links here alone; the four scopes (models / web search / voice / video) are tabs under a fixed "Providers" H1 (the H1 mirrors the sidebar
// entry and must NOT change per scope — the rail already says which scope is
// active).
//
// The header row also owns the unified actions for every scope: one search box
// (piped into each panel as a prop) and one Add button (forwarded to the
// panel's exposed openAdd). Panels render NO header/actions of their own.
//
// Scope switches stay on this route (?tab=) via router.replace — in-page state,
// not history entries — and drop any detail query key so a scope never inherits
// another scope's drill-in. The rail and header stay mounted across switches
// (that keeps the underline indicator's slide animation alive); the panels swap
// below with NO enter animation — the only motion here is the tab indicator's.
//
// Detail views are different: each panel's DetailPane owns its full-width
// rails and cross-slides inside SwapTransition. Keeping this header in normal
// flow would collapse its height as soon as detail opens, yanking [data-view-swap]
// upward mid-slide (and downward on back). Anchoring the header as an absolute
// overlay inside [data-providers-shell] while reserving its measured height on
// the list pane keeps [data-view-swap]'s coordinate origin stationary across
// both directions, and reading the panel's synchronous `view` avoids a one-frame
// wait on async router.replace.
import { computed, onActivated, onBeforeUnmount, onMounted, ref, shallowReactive } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  Button,
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
  PageHeader,
} from '@felinic/ui'
import { Plus, Search } from 'lucide-vue-next'
import ProviderScopeTabs from '@/components/provider-scope-tabs/index.vue'
import { PROVIDER_SCOPES, isProviderScopeTab, type ProviderScopeTab } from '@/lib/provider-scopes'
import ModelsPanel from './models-panel.vue'
import WebSearchPanel from '@/pages/web-search/index.vue'
import VoicePanel from '@/pages/voice/index.vue'
import VideoPanel from '@/pages/video/index.vue'

interface ProviderScopePanelInstance {
  openAdd?: () => void
  readonly view: 'list' | 'detail'
}

const PANELS = {
  models: ModelsPanel,
  'web-search': WebSearchPanel,
  voice: VoicePanel,
  video: VideoPanel,
} satisfies Record<ProviderScopeTab, abstract new (...args: never[]) => ProviderScopePanelInstance>

// Web Search has no add dialog — its list IS the catalog (clicking a template
// card starts configuring it), so the header Add button steps aside there.
// Voice's Add opens the Speaking (TTS) dialog; the Listening section keeps its
// own Add button for the second kind.
const SCOPES_WITH_ADD: readonly ProviderScopeTab[] = ['models', 'voice', 'video']

const route = useRoute()
const router = useRouter()
const { t } = useI18n()

const searchQuery = ref('')
const shellEl = ref<HTMLElement | null>(null)
const headerMeasureEl = ref<HTMLElement | null>(null)
const panelRefs = shallowReactive<Partial<Record<ProviderScopeTab, ProviderScopePanelInstance | null>>>({})
const panelRefSetters = Object.fromEntries(
  PROVIDER_SCOPES.map(({ tab }) => [
    tab,
    (inst: unknown) => {
      const next = (inst as ProviderScopePanelInstance | null) ?? null
      if (panelRefs[tab] !== next) {
        panelRefs[tab] = next
      }
    },
  ]),
) as Record<ProviderScopeTab, (inst: unknown) => void>

const activeTab = computed<ProviderScopeTab>(() =>
  isProviderScopeTab(route.query.tab) ? route.query.tab : 'models',
)

const activeScope = computed(() =>
  PROVIDER_SCOPES.find(scope => scope.tab === activeTab.value) ?? PROVIDER_SCOPES[0],
)

const showAdd = computed(() => SCOPES_WITH_ADD.includes(activeTab.value))

function setTab(tab: ProviderScopeTab): void {
  if (tab === activeTab.value) return
  // replace (not push): an in-page scope switch shouldn't pile onto history;
  // passing only { tab } also clears the previous scope's detail key.
  void router.replace({ query: { tab } }).catch(() => {})
}

// Detail open = current scope's detail query key is set (e.g. ?provider=…).
// Prefer the mounted panel's reactive `view` keyed per tab so the header
// transitions in the exact same flush as SwapTransition without waiting on
// async router.replace or reading a stale KeepAlive panel across tabs.
const routeIsDetail = computed(() => {
  const value = route.query[activeScope.value.detailQueryKey]
  return typeof value === 'string' && value !== ''
})

const isDetail = computed(() => {
  const panel = panelRefs[activeTab.value]
  if (panel) {
    return panel.view === 'detail'
  }
  return routeIsDetail.value
})

function applyHeaderHeight(height: number): void {
  if (height > 0 && shellEl.value) {
    shellEl.value.style.setProperty('--providers-header-offset', `${height}px`)
  }
}

function syncHeaderHeight(): void {
  applyHeaderHeight(headerMeasureEl.value?.getBoundingClientRect().height ?? 0)
}

let headerResizeObserver: ResizeObserver | null = null
onMounted(() => {
  syncHeaderHeight()
  if (typeof ResizeObserver !== 'undefined' && headerMeasureEl.value) {
    headerResizeObserver = new ResizeObserver((entries) => {
      const entry = entries[0]
      const height = entry?.borderBoxSize?.[0]?.blockSize
        ?? headerMeasureEl.value?.getBoundingClientRect().height
        ?? 0
      applyHeaderHeight(height)
    })
    headerResizeObserver.observe(headerMeasureEl.value)
  }
})

onActivated(syncHeaderHeight)

onBeforeUnmount(() => {
  headerResizeObserver?.disconnect()
  headerResizeObserver = null
})
</script>

<template>
  <div
    ref="shellEl"
    data-providers-shell
    class="relative overflow-x-clip"
  >
    <Transition
      :name="isDetail ? 'swap-forward' : 'swap-back'"
      @enter="syncHeaderHeight"
    >
      <div
        v-show="!isDetail"
        data-providers-header
      >
        <div
          ref="headerMeasureEl"
          class="mx-auto max-w-3xl px-4 pt-6 md:px-6 md:pt-10"
        >
          <PageHeader
            :title="t('sidebar.providers')"
            :level="1"
            framed
          >
            <template #actions>
              <div class="flex items-center">
                <div class="w-44 sm:w-56">
                  <InputGroup class="w-full">
                    <InputGroupAddon align="inline-start">
                      <Search class="size-3.5 text-muted-foreground" />
                    </InputGroupAddon>
                    <InputGroupInput
                      v-model="searchQuery"
                      :placeholder="t('provider.searchPlaceholder')"
                    />
                  </InputGroup>
                </div>
                <!-- Add folds away with an animated width instead of v-if: on Web
                     Search — the one scope with no add flow — the button folds up
                     and the search box GLIDES right instead of jumping. ponytail:
                     width:auto can't transition and the 0fr-grid trick needs a
                     definite container width (a shrink-to-fit flex item resolves
                     0fr to content width), so the cap is max-width with an 8rem
                     ceiling — every locale's Add label runs ~5rem; a wider label
                     would clip while shown. The gap lives on this wrapper's margin
                     so it collapses in the same motion. inert keeps the clipped
                     button out of tab order while folded (`true`/`undefined`,
                     never the string "false" — inert is presence-based). -->
                <div
                  class="overflow-hidden transition-[max-width,margin] duration-200 ease-out"
                  :class="showAdd ? 'ml-2 max-w-32' : 'ml-0 max-w-0'"
                  :inert="!showAdd ? true : undefined"
                >
                  <Button
                    class="whitespace-nowrap transition-opacity duration-200"
                    :class="showAdd ? 'opacity-100' : 'opacity-0'"
                    @click="panelRefs[activeTab]?.openAdd?.()"
                  >
                    <Plus class="size-4" />
                    {{ t('provider.addBtn') }}
                  </Button>
                </div>
              </div>
            </template>
          </PageHeader>
          <ProviderScopeTabs
            :model-value="activeTab"
            @update:model-value="setTab"
          />
        </div>
      </div>
    </Transition>

    <KeepAlive>
      <component
        :is="PANELS[activeTab]"
        :key="activeTab"
        :ref="panelRefSetters[activeTab]"
        :search-query="searchQuery"
      />
    </KeepAlive>
  </div>
</template>

<style>
[data-providers-header] {
  position: absolute;
  inset-inline: 0;
  top: 0;
  z-index: var(--z-raised);
}
[data-providers-header].swap-forward-leave-active,
[data-providers-header].swap-back-leave-active {
  bottom: auto;
  pointer-events: none;
}
[data-providers-shell] > [data-view-swap] {
  display: flow-root;
}
[data-providers-shell] > [data-view-swap] > :not(:has([data-settings-detail-back])) {
  margin-top: var(--providers-header-offset, 0px);
}
</style>
