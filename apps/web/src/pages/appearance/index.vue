<template>
  <PageShell :title="t('settings.appearance.title')">
    <div class="space-y-8">
      <SettingsSection :title="t('settings.appearance.interface')">
        <SettingsRow :label="t('settings.language')">
          <Select
            :model-value="language"
            @update:model-value="(value) => value && setLanguage(value as Locale)"
          >
            <SelectTrigger size="sm">
              <SelectValue :placeholder="t('settings.languagePlaceholder')" />
            </SelectTrigger>
            <SelectContent
              align="end"
              :align-offset="0"
            >
              <SelectItem value="en">
                {{ t('settings.langEn') }}
              </SelectItem>
              <SelectItem value="zh">
                {{ t('settings.langZh') }}
              </SelectItem>
              <SelectItem value="ja">
                {{ t('settings.langJa') }}
              </SelectItem>
            </SelectContent>
          </Select>
        </SettingsRow>

        <SettingsRow :label="t('settings.theme')">
          <SegmentedControl
            :model-value="theme"
            :items="themeItems"
            :aria-label="t('settings.theme')"
            @update:model-value="(value) => setTheme(value as ThemePreference)"
          >
            <template #item="{ item }">
              <component
                :is="themeIcons[item.value]"
                class="size-4"
              />
            </template>
          </SegmentedControl>
        </SettingsRow>

        <SettingsRow :label="t('settings.appearance.colorScheme')">
          <Select
            :model-value="colorScheme"
            @update:model-value="(value) => value && setColorScheme(value as ColorSchemeId)"
          >
            <SelectTrigger
              size="sm"
              class="min-w-36"
            >
              <span class="flex min-w-0 items-center gap-1.5">
                <span
                  class="flex size-5 shrink-0 items-center justify-center rounded-sm border border-border text-[10px] font-semibold"
                  :style="{ backgroundColor: previewSwatches(currentColorScheme)[0] }"
                >
                  <span :style="{ color: previewSwatches(currentColorScheme)[4] }">Aa</span>
                </span>
                <span class="truncate">
                  {{ t(currentColorScheme.labelKey) }}
                </span>
              </span>
            </SelectTrigger>
            <SelectContent
              align="end"
              :align-offset="0"
            >
              <SelectItem
                v-for="scheme in colorSchemes"
                :key="scheme.id"
                :value="scheme.id"
              >
                <span class="flex min-w-0 items-center gap-1.5">
                  <span
                    class="flex size-6 shrink-0 items-center justify-center rounded-sm border border-border text-[11px] font-semibold"
                    :style="{ backgroundColor: previewSwatches(scheme)[0] }"
                  >
                    <span :style="{ color: previewSwatches(scheme)[4] }">Aa</span>
                  </span>
                  <span class="truncate">
                    {{ t(scheme.labelKey) }}
                  </span>
                </span>
              </SelectItem>
            </SelectContent>
          </Select>
        </SettingsRow>
      </SettingsSection>

      <SettingsSection :title="t('settings.appearance.typography')">
        <SettingsRow
          :label="t('settings.appearance.uiFontSize')"
          :description="t('settings.appearance.uiFontSizeDescription')"
        >
          <Input
            id="ui-font-size"
            type="number"
            min="12"
            max="20"
            step="1"
            :model-value="uiFontSizeDraft"
            :placeholder="String(DEFAULT_UI_FONT_SIZE_PX)"
            class="h-8 w-20 tabular-nums"
            @update:model-value="(value) => updateUiFontSizeDraft(value)"
            @change="commitUiFontSizeDraft"
            @blur="commitUiFontSizeDraft"
            @keydown.enter="commitUiFontSizeDraft"
          />
        </SettingsRow>

        <SettingsRow
          :label="t('settings.appearance.codeFontSize')"
          :description="t('settings.appearance.codeFontSizeDescription')"
        >
          <Input
            id="code-font-size"
            type="number"
            min="11"
            max="20"
            step="1"
            :model-value="codeFontSizeDraft"
            :placeholder="String(DEFAULT_CODE_FONT_SIZE_PX)"
            class="h-8 w-20 tabular-nums"
            @update:model-value="(value) => updateCodeFontSizeDraft(value)"
            @change="commitCodeFontSizeDraft"
            @blur="commitCodeFontSizeDraft"
            @keydown.enter="commitCodeFontSizeDraft"
          />
        </SettingsRow>

        <SettingsRow
          :label="t('settings.appearance.uiFontFamily')"
          :description="t('settings.appearance.uiFontFamilyDescription')"
        >
          <Input
            id="ui-font-family"
            :model-value="uiFontFamilyDraft"
            :placeholder="defaultUiFontFamily"
            class="h-8 w-48 font-mono text-xs"
            @update:model-value="(value) => updateUiFontFamilyDraft(value)"
            @change="commitUiFontFamilyDraft"
            @blur="commitUiFontFamilyDraft"
            @keydown.enter="commitUiFontFamilyDraft"
          />
        </SettingsRow>

        <SettingsRow
          :label="t('settings.appearance.codeFontFamily')"
          :description="t('settings.appearance.codeFontFamilyDescription')"
        >
          <Input
            id="code-font-family"
            :model-value="codeFontFamilyDraft"
            :placeholder="defaultCodeFontFamily"
            class="h-8 w-48 font-mono text-xs"
            @update:model-value="(value) => updateCodeFontFamilyDraft(value)"
            @change="commitCodeFontFamilyDraft"
            @blur="commitCodeFontFamilyDraft"
            @keydown.enter="commitCodeFontFamilyDraft"
          />
        </SettingsRow>
      </SettingsSection>

      <!-- Rarely-touched code/diagram theming lives behind an ActionCard that
           opens a focused DIALOG — not a push-in sub-page. The structural rule
           from the design pass: a facet of the current page never grows a
           sub-level (sub-pages are reserved for entities like a Provider); a
           dialog keeps the page visibly underneath, so "where am I" never
           arises and no back-button chrome is needed. -->
      <section class="space-y-2.5">
        <h2 class="px-2 text-label font-medium text-muted-foreground">
          {{ t('settings.appearance.advanced') }}
        </h2>
        <ActionCard
          :title="t('settings.appearance.advancedEntryTitle')"
          @click="advancedOpen = true"
        >
          <template #icon>
            <SlidersHorizontal />
          </template>
        </ActionCard>
      </section>
    </div>
  </PageShell>

  <!-- Code & diagrams dialog. DialogPanel (capped focused shell), NOT
       DialogScrollContent — that variant scrolls the whole overlay, so a tall
       dialog grows past the viewport and drags the PAGE scrollbar (rejected).
       Only the DialogBody row scrolls. Stays mounted outside any conditional
       so open/close never loses highlighter state. -->
  <Dialog v-model:open="advancedOpen">
    <DialogPanel>
      <DialogHeader>
        <DialogTitle>{{ t('settings.appearance.advancedEntryTitle') }}</DialogTitle>
      </DialogHeader>

      <DialogBody class="space-y-6">
        <section class="space-y-3">
          <h3 class="text-sm font-medium text-foreground">
            {{ t('settings.appearance.codeHighlight') }}
          </h3>
          <p class="text-xs text-muted-foreground">
            {{ t('settings.appearance.codeHighlightDescription') }}
          </p>
          <div class="grid gap-3 sm:grid-cols-2">
            <div class="space-y-2">
              <SearchableSelectPopover
                v-model="shikiThemeLightSelection"
                :options="lightShikiThemeOptions"
                :placeholder="t('settings.appearance.shikiThemeLight')"
                :aria-label="t('settings.appearance.shikiThemeLight')"
                :search-placeholder="t('settings.appearance.shikiThemeSearch')"
                :search-aria-label="t('settings.appearance.shikiThemeSearch')"
                :empty-text="t('settings.appearance.shikiThemeEmpty')"
                :show-group-headers="false"
              />
              <!-- NOT pointer-events-none/inert (unlike the mermaid preview):
                   the code sample overflows horizontally and its scrollbar
                   must stay draggable. The content is static highlighted
                   markup — nothing focusable to fence off. -->
              <div
                class="typography-code-preview shiki-preview overflow-hidden"
                :style="codeFontPreviewStyle"
              >
                <!-- eslint-disable vue/no-v-html -->
                <div
                  class="overflow-x-auto"
                  v-html="codeFontPreviewLightHtml"
                />
                <!-- eslint-enable vue/no-v-html -->
              </div>
            </div>
            <div class="space-y-2">
              <SearchableSelectPopover
                v-model="shikiThemeDarkSelection"
                :options="darkShikiThemeOptions"
                :placeholder="t('settings.appearance.shikiThemeDark')"
                :aria-label="t('settings.appearance.shikiThemeDark')"
                :search-placeholder="t('settings.appearance.shikiThemeSearch')"
                :search-aria-label="t('settings.appearance.shikiThemeSearch')"
                :empty-text="t('settings.appearance.shikiThemeEmpty')"
                :show-group-headers="false"
              />
              <div
                class="typography-code-preview shiki-preview overflow-hidden"
                :style="codeFontPreviewStyle"
              >
                <!-- eslint-disable vue/no-v-html -->
                <div
                  class="overflow-x-auto"
                  v-html="codeFontPreviewDarkHtml"
                />
                <!-- eslint-enable vue/no-v-html -->
              </div>
            </div>
          </div>
        </section>

        <section class="space-y-3">
          <div class="flex min-h-[2.25rem] items-center justify-between gap-4">
            <div class="min-w-0">
              <h3 class="text-sm font-medium text-foreground">
                {{ t('settings.appearance.mermaidTheme') }}
              </h3>
              <p class="mt-0.5 text-xs text-muted-foreground">
                {{ t('settings.appearance.mermaidThemeDescription') }}
              </p>
            </div>
            <div class="shrink-0">
              <Select
                :model-value="mermaidTheme"
                @update:model-value="(value) => isMermaidTheme(value) && setMermaidTheme(value)"
              >
                <SelectTrigger
                  size="sm"
                  class="min-w-36"
                >
                  <SelectValue>
                    {{ mermaidThemeLabels[mermaidTheme] }}
                  </SelectValue>
                </SelectTrigger>
                <SelectContent
                  align="end"
                  :align-offset="0"
                >
                  <SelectItem
                    v-for="value in MERMAID_THEMES"
                    :key="value"
                    :value="value"
                  >
                    {{ mermaidThemeLabels[value] }}
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <!-- min-h reserves the diagram's height BEFORE mermaid's async SVG
               lands — without it the container starts at 0 and the dialog
               visibly jumps taller a frame later. 25.5rem (408px) is DERIVED
               from a devtools measurement of the rendered dialog, not guessed:
               the whole section measured 611×458; minus the header block
               (title 20 + mt-0.5 2 + description 16 = 38px) and the space-y-3
               gap (12px) leaves 458 − 38 − 12 = 408px for this preview. Valid
               at the dialog's sm:max-w-2xl width — mermaid scales the SVG to
               container width, so if the dialog width or the preview content
               changes, re-measure. -->
          <div class="appearance-mermaid-preview pointer-events-none min-h-[25.5rem]">
            <MarkdownRender
              :content="MERMAID_PREVIEW_CONTENT"
              :is-dark="isDark"
              :typewriter="false"
              :fade="false"
              :mermaid-props="MERMAID_PREVIEW_PROPS"
              custom-id="appearance-mermaid-preview"
            />
          </div>
        </section>
      </DialogBody>
    </DialogPanel>
  </Dialog>

  <!-- Mermaid warm-up: renders the same diagram once, offscreen, as soon as
       this page mounts — so mermaid's heavy dynamic import + first-render
       initialization happen BEFORE the user opens the dialog, not during its
       opening frame (the cause of the visible late-pop). Unmounted permanently
       on first dialog open: by then the module is loaded and the real
       instance takes over. fixed + offscreen (not display:none) because
       mermaid must measure layout to render. -->
  <div
    v-if="mermaidWarmup"
    class="pointer-events-none fixed top-0 -left-[999rem] w-[40rem]"
    aria-hidden="true"
  >
    <MarkdownRender
      :content="MERMAID_PREVIEW_CONTENT"
      :is-dark="isDark"
      :typewriter="false"
      :fade="false"
      :mermaid-props="MERMAID_PREVIEW_PROPS"
      custom-id="appearance-mermaid-warmup"
    />
  </div>
</template>

<script setup lang="ts">
import type { Component } from 'vue'

import {
  ActionCard,
  Dialog,
  DialogBody,
  DialogHeader,
  DialogPanel,
  DialogTitle,
  Input,
  SegmentedControl,
  type SegmentedItem,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@felinic/ui'
import { Monitor, Moon, SlidersHorizontal, Sun } from 'lucide-vue-next'
import { storeToRefs } from 'pinia'
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import MarkdownRender, { enableMermaid, setCustomComponents } from 'markstream-vue'
import { PageShell, SettingsRow, SettingsSection } from '@felinic/ui'
import { useShikiHighlighter } from '@/composables/useShikiHighlighter'
import type { Locale } from '@/i18n'
import type { BundledTheme } from 'shiki'
import SearchableSelectPopover from '@/components/searchable-select-popover/index.vue'
import type { SearchableSelectOption } from '@/components/searchable-select-popover/index.vue'
import ThemedMermaidBlock from '@/components/themed-mermaid-block/index.vue'
import { colorSchemes, type ColorSchemeId, type ColorSchemeOption } from '@/constants/color-schemes'
import { MERMAID_THEMES, type MermaidTheme, useSettingsStore, type ThemePreference } from '@/store/settings'
import { isMermaidTheme } from '@/store/settings/mermaid'
import { listBundledShikiThemes } from '@/store/settings/shiki-theme'
import { cssCodeFontFamilyStyleValue, DEFAULT_CODE_FONT_SIZE_PX, DEFAULT_UI_FONT_SIZE_PX, normalizeCodeFontSizePx } from '@/store/settings/typography'

enableMermaid()
setCustomComponents({ mermaid: ThemedMermaidBlock })

const MERMAID_PREVIEW_CONTENT = `\`\`\`mermaid
pie
  title Theme palette
  "Chat" : 38
  "Memory" : 27
  "Tools" : 20
  "Skills" : 15
\`\`\``

// Hide every chrome control on the preview's mermaid block; we want just the
// bare diagram, no header, no toolbar.
const MERMAID_PREVIEW_PROPS = {
  showHeader: false,
  showModeToggle: false,
  showCopyButton: false,
  showExportButton: false,
  showFullscreenButton: false,
  showCollapseButton: false,
  showZoomControls: false,
  enableMermaidInteractions: false,
} as const

const { t } = useI18n()
const settingsStore = useSettingsStore()
const { language, theme, colorScheme, uiFontFamily, codeFontFamily, uiFontSizePx, codeFontSizePx, shikiThemeLight, shikiThemeDark, mermaidTheme, defaultUiFontFamily, defaultCodeFontFamily, isDark } = storeToRefs(settingsStore)
const { setLanguage, setTheme, setColorScheme, setUiFontFamily, setCodeFontFamily, setUiFontSizePx, setCodeFontSizePx, setShikiTheme, setMermaidTheme } = settingsStore

// Code & diagrams dialog visibility. The two preview blocks live in the dialog
// but share this component's script (highlighter instances, draft state), so
// opening/closing is pure visibility — nothing re-initializes.
const advancedOpen = ref(false)

// Offscreen mermaid warm-up (see the template comment). Alive until the first
// dialog open, then dropped for good — the warm render's only job is to pull
// in the mermaid module and pay its init cost ahead of time.
const mermaidWarmup = ref(true)
watch(advancedOpen, (open) => {
  if (open) mermaidWarmup.value = false
})

const mermaidThemeLabels: Record<MermaidTheme, string> = {
  auto: 'Auto',
  default: 'Default',
  dark: 'Dark',
  forest: 'Forest',
  neutral: 'Neutral',
}

const allShikiThemes = listBundledShikiThemes()
const lightShikiThemeOptions = computed<SearchableSelectOption[]>(() =>
  allShikiThemes
    .filter(theme => theme.type === 'light')
    .map(theme => ({ value: theme.id, label: theme.displayName, keywords: [theme.id] })),
)
const darkShikiThemeOptions = computed<SearchableSelectOption[]>(() =>
  allShikiThemes
    .filter(theme => theme.type === 'dark')
    .map(theme => ({ value: theme.id, label: theme.displayName, keywords: [theme.id] })),
)
const shikiThemeLightSelection = computed<string>({
  get: () => shikiThemeLight.value,
  set: (value) => setShikiTheme('light', value as BundledTheme),
})
const shikiThemeDarkSelection = computed<string>({
  get: () => shikiThemeDark.value,
  set: (value) => setShikiTheme('dark', value as BundledTheme),
})

const currentColorScheme = computed(() =>
  colorSchemes.find(scheme => scheme.id === colorScheme.value) ?? colorSchemes[0],
)

function previewSwatches(scheme: ColorSchemeOption) {
  return isDark.value ? scheme.darkSwatches : scheme.swatches
}

const themeItems: SegmentedItem<ThemePreference>[] = [
  { value: 'system' },
  { value: 'light' },
  { value: 'dark' },
]
const themeIcons: Record<ThemePreference, Component> = {
  system: Monitor,
  light: Sun,
  dark: Moon,
}

const uiFontSizeDraft = ref(String(uiFontSizePx.value))
const codeFontSizeDraft = ref(String(codeFontSizePx.value))
const uiFontFamilyDraft = ref(uiFontFamily.value)
const codeFontFamilyDraft = ref(codeFontFamily.value)
// Each variant renders in its own highlighter against its picked theme, so
// the two previews always show the chosen light + dark side-by-side regardless
// of the interface mode (mirrors Claude Code's Code-appearance panel).
const codeFontPreviewLight = useShikiHighlighter()
const codeFontPreviewDark = useShikiHighlighter()
const codeFontPreviewCode = `function greet(name: string) {
  return \`Hi, \${name}\`
}`
const codeFontPreviewFallback = `<pre><code>${codeFontPreviewCode}</code></pre>`
const codeFontPreviewLightHtml = computed(() => codeFontPreviewLight.html.value || codeFontPreviewFallback)
const codeFontPreviewDarkHtml = computed(() => codeFontPreviewDark.html.value || codeFontPreviewFallback)
const codeFontPreviewStyle = computed(() => ({
  '--typography-code-preview-font-family': cssCodeFontFamilyStyleValue(codeFontFamilyDraft.value),
  '--typography-code-preview-font-size': `${normalizeCodeFontSizePx(codeFontSizeDraft.value)}px`,
}))

function renderCodeFontPreview() {
  // Both halves of each preview use the SAME picked theme. The design system's
  // `.dark .shiki span` !important rule forces every span color to var(--shiki-dark)
  // in dark interface mode; pinning both halves means that variable already equals
  // the chosen theme's colors, so the cascade override is a visual no-op and each
  // preview stays true to its picked theme regardless of interface mode.
  const light = shikiThemeLight.value as BundledTheme
  const dark = shikiThemeDark.value as BundledTheme
  void codeFontPreviewLight.highlightLanguage(codeFontPreviewCode, 'typescript', {
    themes: { light, dark: light },
  })
  void codeFontPreviewDark.highlightLanguage(codeFontPreviewCode, 'typescript', {
    themes: { light: dark, dark },
  })
}

onMounted(() => {
  renderCodeFontPreview()
})

watch(uiFontSizePx, (value) => { uiFontSizeDraft.value = String(value) })
watch(codeFontSizePx, (value) => { codeFontSizeDraft.value = String(value) })
watch(uiFontFamily, (value) => { uiFontFamilyDraft.value = value })
watch(codeFontFamily, (value) => { codeFontFamilyDraft.value = value })
watch([shikiThemeLight, shikiThemeDark], () => { renderCodeFontPreview() })

function updateUiFontSizeDraft(value: string | number) { uiFontSizeDraft.value = String(value) }
function updateCodeFontSizeDraft(value: string | number) { codeFontSizeDraft.value = String(value) }
function updateUiFontFamilyDraft(value: string | number) { uiFontFamilyDraft.value = String(value) }
function updateCodeFontFamilyDraft(value: string | number) { codeFontFamilyDraft.value = String(value) }

function commitUiFontSizeDraft() {
  const draft = uiFontSizeDraft.value.trim()
  if (draft === '' || !Number.isFinite(Number(draft))) {
    uiFontSizeDraft.value = String(uiFontSizePx.value)
    return
  }
  setUiFontSizePx(draft)
  uiFontSizeDraft.value = String(uiFontSizePx.value)
}

function commitCodeFontSizeDraft() {
  const draft = codeFontSizeDraft.value.trim()
  if (draft === '' || !Number.isFinite(Number(draft))) {
    codeFontSizeDraft.value = String(codeFontSizePx.value)
    return
  }
  setCodeFontSizePx(draft)
  codeFontSizeDraft.value = String(codeFontSizePx.value)
}

function commitUiFontFamilyDraft() {
  setUiFontFamily(uiFontFamilyDraft.value)
  uiFontFamilyDraft.value = uiFontFamily.value
}

function commitCodeFontFamilyDraft() {
  setCodeFontFamily(codeFontFamilyDraft.value)
  codeFontFamilyDraft.value = codeFontFamily.value
}
</script>

<style scoped>
/* Strip markstream-vue's card chrome off the appearance preview so only the
   bare diagram shows — no border, no surface background, no rounding. */
.appearance-mermaid-preview :deep(.mermaid-block-container) {
  border: 0;
  border-radius: 0;
  background: transparent;
}
/* markstream windows tall diagrams into a fixed min-height preview (360px) and
   clips the overflow, which cut off the bottom of the square-ish pie. Raise the
   window so the whole pie (~450px at full width) shows; min-height overrides
   markstream's inline height without fighting its resize transitions. */
.appearance-mermaid-preview :deep(.mermaid-preview-area) {
  background: transparent;
  min-height: 470px;
}

/* The shiki preview keeps the theme's inline background-color on <pre>, so
   the parent typography-code-preview rule's `padding: 0` would crowd the code
   against the corners. Re-add comfortable padding inside the themed box, and
   derive the rounded outline from the theme's own text color (via currentColor)
   so it stays stable across UI mode flips — UI tokens like --color-border
   adapt with light/dark, which we want for page chrome but not for these
   theme previews. Line numbers follow the same idea so they stay legible on
   any chosen theme. */
.shiki-preview :deep(pre) {
  /* width:max-content + min-width:100% makes the themed box grow to the code's
     full length so its background and inset ring paint UNDER the overflowing
     text. Without this the <pre> stays at the overflow-x-auto wrapper's width,
     and scrolling right exposes the page behind the theme color (the box only
     ever painted the first viewport-width of code). */
  width: max-content;
  min-width: 100%;
  padding: 0.5rem 0.75rem;
  border-radius: 0.375rem;
  box-shadow: inset 0 0 0 1px color-mix(in srgb, currentColor 18%, transparent);
}
.shiki-preview :deep(.line::before) {
  color: currentColor;
  opacity: 0.45;
}
</style>
