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
// rails, so while a detail is open this header steps aside entirely (v-show,
// not v-if — the rail's measured indicator survives) and the panel renders
// bare, exactly as it did when these were standalone pages.
import { computed, ref, type Component, type ComponentPublicInstance } from 'vue'
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

const PANELS: Record<ProviderScopeTab, Component> = {
  models: ModelsPanel,
  'web-search': WebSearchPanel,
  voice: VoicePanel,
  video: VideoPanel,
}

// Web Search has no add dialog — its list IS the catalog (clicking a template
// card starts configuring it), so the header Add button steps aside there.
// Voice's Add opens the Speaking (TTS) dialog; the Listening section keeps its
// own Add button for the second kind.
const SCOPES_WITH_ADD: readonly ProviderScopeTab[] = ['models', 'voice', 'video']

const route = useRoute()
const router = useRouter()
const { t } = useI18n()

const searchQuery = ref('')
const panelRef = ref<(ComponentPublicInstance & { openAdd?: () => void }) | null>(null)

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
// The header hides while any detail is open so the DetailPane keeps its own
// full-width gutters instead of double-padding inside this page's column.
const isDetail = computed(() => {
  const value = route.query[activeScope.value.detailQueryKey]
  return typeof value === 'string' && value !== ''
})
</script>

<template>
  <div>
    <div
      v-show="!isDetail"
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
                @click="panelRef?.openAdd?.()"
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

    <KeepAlive>
      <component
        :is="PANELS[activeTab]"
        :key="activeTab"
        ref="panelRef"
        :search-query="searchQuery"
      />
    </KeepAlive>
  </div>
</template>
