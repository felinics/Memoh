<template>
  <section class="mx-auto flex min-h-full max-w-3xl flex-col px-6 py-10">
    <!-- About is sparse: keep the title attached to its cards and float the whole
         group in the upper-middle (biased above center), unlike the denser,
         top-aligned Settings pages. -->
    <div class="flex flex-1 flex-col justify-center gap-8 pb-[12vh]">
      <h1 class="px-2 text-lg font-semibold">
        {{ $t('sidebar.about') }}
      </h1>

      <!-- Identity: logo, name, tagline, blurb and the resource links all live in
           one card so About reads like the rest of Settings (cf. Profile). -->
      <SettingsSection>
        <div class="p-5">
          <div class="flex items-center gap-4">
            <div class="flex size-14 shrink-0 items-center justify-center rounded-xl border border-border bg-background">
              <img
                src="/logo.svg"
                alt="Memoh"
                class="size-9"
              >
            </div>
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2">
                <h2 class="text-base font-semibold text-foreground">
                  Memoh
                </h2>
                <Badge
                  v-if="displayVersion"
                  variant="secondary"
                  size="sm"
                  class="font-medium"
                >
                  {{ $t('settings.versionTag', { version: displayVersion }) }}
                </Badge>
              </div>
              <p class="mt-0.5 text-[13px] leading-[18px] tracking-[-0.08px] font-[360] text-muted-foreground">
                {{ $t('about.tagline') }}
              </p>
            </div>
          </div>

          <p class="mt-4 text-[13px] leading-[19px] tracking-[-0.06px] font-[380] text-muted-foreground">
            {{ $t('about.description') }}
          </p>

          <div class="mt-5 flex flex-wrap gap-2">
            <Button
              v-for="link in links"
              :key="link.href"
              as="a"
              :href="link.href"
              target="_blank"
              rel="noopener noreferrer"
              variant="outline"
              size="sm"
              class="text-[13px] leading-[18px] tracking-[-0.08px] font-[420] text-foreground/82"
            >
              <component
                :is="link.icon"
                class="size-4"
              />
              {{ $t(link.labelKey) }}
            </Button>
          </div>
        </div>
      </SettingsSection>

      <!-- Updates: status as the row label, actions on the right — the standard
           Settings row pattern. Browser checks the OSS server release; Desktop
           delegates app checks/download/install to Electron main over preload. -->
      <SettingsSection :title="$t('about.updatesSection')">
        <SettingsRow
          :label="updateLabel"
          :description="updateDesc"
        >
          <div class="flex items-center gap-1.5">
            <template v-if="desktopUpdates">
              <Button
                v-if="desktopUpdate?.status !== 'unavailable'"
                variant="secondary"
                size="sm"
                :disabled="desktopUpdateActionDisabled"
                :loading="desktopUpdateActionLoading"
                loading-mode="icon"
                @click="runDesktopUpdateAction"
              >
                <Download v-if="desktopUpdate?.status === 'available'" />
                <RotateCcw v-else-if="desktopUpdate?.status === 'downloaded'" />
                <RefreshCw v-else />
                {{ desktopUpdateActionLabel }}
              </Button>
            </template>
            <template v-else-if="update.hasUpdate">
              <Button
                v-if="update.releaseBody"
                variant="ghost"
                size="sm"
                class="font-[430] text-foreground/88"
                @click="notesOpen = true"
              >
                {{ $t('about.whatsChanged') }}
              </Button>
              <Button
                as="a"
                :href="update.releaseUrl"
                target="_blank"
                rel="noopener noreferrer"
                size="sm"
                class="font-[430]"
              >
                {{ $t('about.viewOnGitHub') }}
              </Button>
            </template>
            <Button
              v-else
              variant="secondary"
              size="sm"
              :loading="update.checking"
              loading-mode="icon"
              @click="recheck"
            >
              <RefreshCw class="size-3.5" />
              {{ update.checking ? $t('about.checking') : $t('about.checkForUpdates') }}
            </Button>
          </div>
        </SettingsRow>
      </SettingsSection>

      <section
        v-if="desktopShell"
        class="space-y-2.5"
      >
        <ActionCard
          :title="$t('about.advanced')"
          @click="advancedOpen = true"
        >
          <template #icon>
            <SlidersHorizontal />
          </template>
        </ActionCard>
      </section>
    </div>

    <!-- Footer meta near the bottom; lifted with a transform so nudging it never
         shifts the centered group above (padding/margin would shrink the flex-1
         spacer and pull the cards up with it). -->
    <div class="flex shrink-0 -translate-y-[6vh] flex-col items-center gap-1 pt-8 text-center">
      <p class="text-[11px] text-muted-foreground">
        {{ $t('about.license') }}
      </p>
      <p
        v-if="commitHash"
        class="font-mono text-[11px] text-muted-foreground"
      >
        {{ commitHash }}
      </p>
    </div>

    <!-- Release notes live behind a button and scroll internally so the dialog
         never grows past the viewport. -->
    <Dialog
      v-if="!desktopUpdates"
      v-model:open="notesOpen"
    >
      <DialogContent class="flex max-h-[80vh] w-full max-w-2xl flex-col gap-0 overflow-hidden p-0 sm:max-w-2xl">
        <DialogHeader class="border-b border-border px-6 py-4 text-left">
          <DialogTitle>{{ $t('about.releaseNotes') }}</DialogTitle>
          <DialogDescription v-if="update.latestVersion">
            {{ $t('about.newVersionTitle', { version: update.latestVersion }) }}
          </DialogDescription>
        </DialogHeader>

        <div
          class="about-notes prose prose-sm dark:prose-invert min-h-0 max-w-none flex-1 overflow-y-auto px-6 py-4 text-sm leading-relaxed *:first:mt-0"
        >
          <MarkdownRender
            v-if="update.releaseBody"
            :content="cleanMarkdownBody(update.releaseBody)"
            :is-dark="isDark"
            :typewriter="false"
            :fade="false"
            :show-tooltips="false"
            :code-block-dark-theme="codeBlockTheme.dark"
            :code-block-light-theme="codeBlockTheme.light"
            custom-id="release-notes"
          />
        </div>

        <DialogFooter class="border-t border-border px-6 py-3">
          <Button
            as="a"
            :href="update.releaseUrl"
            target="_blank"
            rel="noopener noreferrer"
            variant="outline"
          >
            {{ $t('about.viewOnGitHub') }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <Dialog v-model:open="advancedOpen">
      <DialogPanel width="lg">
        <DialogHeader>
          <DialogTitle>{{ $t('about.advanced') }}</DialogTitle>
          <DialogDescription>{{ $t('about.advancedDescription') }}</DialogDescription>
        </DialogHeader>
        <DialogBody>
          <SettingsSection>
            <SettingsRow
              :label="$t('about.serverConnection')"
              :description="$t('about.serverConnectionDescription')"
            >
              <Button
                variant="outline"
                size="sm"
                @click="openServerConnection"
              >
                {{ $t('about.configureServer') }}
              </Button>
            </SettingsRow>
          </SettingsSection>
        </DialogBody>
      </DialogPanel>
    </Dialog>
  </section>
</template>

<script setup lang="ts">
import type { Component } from 'vue'
import { computed, inject, onBeforeUnmount, onMounted, ref } from 'vue'
import { storeToRefs } from 'pinia'
import { useDark } from '@vueuse/core'
import { BookOpen, Download, Github, MessageSquare, RefreshCw, RotateCcw, SlidersHorizontal } from 'lucide-vue-next'
import {
  ActionCard,
  Badge,
  Button,
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
  toast,
} from '@felinic/ui'
import MarkdownRender from 'markstream-vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import ChatCodeBlock from '@/pages/home/components/chat-code-block.vue'
import { registerSharedMarkdownComponents } from '@/components/markdown'
import {
  DesktopShellKey,
  DesktopUpdatesKey,
  type DesktopUpdateInfo,
  type DesktopUpdateState,
} from '@/lib/desktop-shell'
import { SettingsRow, SettingsSection } from '@felinic/ui'
import { useCapabilitiesStore } from '@/store/capabilities'
import { useSettingsStore } from '@/store/settings'
import { useUpdateStore } from '@/store/update'

const GITHUB_REPO = 'felinics/memoh'

// Release notes are arbitrary upstream markdown and can contain fenced code
// blocks. markstream 2.0's built-in code block needs the optional stream-diffs
// peer (not installed) and otherwise falls back to a plain <pre>, so give this
// scope the same design-system code block chat and file preview already use.
// Idempotent-guarded by the registry; the appearance mermaid scopes need no
// registration — their content is mermaid-only, served by the global override.
registerSharedMarkdownComponents('release-notes', { code_block: ChatCodeBlock, shell: ChatCodeBlock })

interface ResourceLink {
  icon: Component
  labelKey: string
  href: string
}

const { t } = useI18n()
const router = useRouter()
const desktopShell = inject(DesktopShellKey, false)
const desktopUpdates = inject(DesktopUpdatesKey, undefined)
const desktopUpdateInfo = ref<DesktopUpdateInfo | null>(null)
const desktopUpdate = ref<DesktopUpdateState | null>(null)
let stopDesktopUpdateListener: (() => void) | undefined

const capabilitiesStore = useCapabilitiesStore()
const { serverVersion, commitHash } = storeToRefs(capabilitiesStore)
const normalizedServerVersion = computed(() => (serverVersion.value ?? '').replace(/^v/i, ''))
const displayVersion = computed(() => (
  desktopUpdateInfo.value?.version.replace(/^v/i, '')
  || normalizedServerVersion.value
))

const update = useUpdateStore()
const settingsStore = useSettingsStore()
const isDark = useDark()
const codeBlockTheme = computed(() => ({
  light: settingsStore.shikiThemeLight,
  dark: settingsStore.shikiThemeDark,
}))

const links: ResourceLink[] = [
  { icon: Github, labelKey: 'about.github', href: `https://github.com/${GITHUB_REPO}` },
  { icon: BookOpen, labelKey: 'about.docs', href: 'https://docs.memoh.ai' },
  { icon: MessageSquare, labelKey: 'about.feedback', href: `https://github.com/${GITHUB_REPO}/issues` },
]

// Update row copy: the label states the headline status, the description carries
// the running version or a hint, depending on whether a check has run yet.
const updateLabel = computed(() => {
  if (desktopUpdates) {
    const state = desktopUpdate.value
    if (!state) return t('about.currentVersion', { version: displayVersion.value || '—' })
    if (state.status === 'up-to-date') return t('about.upToDate')
    if (['available', 'downloading', 'downloaded'].includes(state.status) && state.latestVersion) {
      return t('about.newVersionTitle', { version: state.latestVersion })
    }
    if (state.status === 'error') return t('about.desktopUpdateFailed')
    if (state.status === 'unavailable') return t('about.desktopUpdatesUnavailable')
    return t('about.currentVersion', { version: state.currentVersion })
  }
  if (update.hasUpdate) return t('about.newVersionTitle', { version: update.latestVersion })
  if (update.checked) return t('about.upToDate')
  return t('about.currentVersion', { version: normalizedServerVersion.value || '—' })
})
const updateDesc = computed(() => {
  if (desktopUpdates) {
    const state = desktopUpdate.value
    if (!state || state.status === 'idle') return t('about.desktopCheckHint')
    if (state.status === 'checking') return t('about.checking')
    if (state.status === 'up-to-date') {
      return t('about.currentVersion', { version: state.currentVersion })
    }
    if (state.status === 'available') return t('about.desktopUpdateAvailableDesc')
    if (state.status === 'downloading') {
      return t('about.downloadingUpdate', { progress: state.progress ?? 0 })
    }
    if (state.status === 'downloaded') return t('about.desktopUpdateDownloadedDesc')
    if (state.status === 'error') return state.error || t('about.desktopUpdateFailed')
    return t('about.desktopUpdatesUnavailableDesc')
  }
  if (update.hasUpdate) return t('about.newVersionDesc')
  if (update.checked) {
    return normalizedServerVersion.value
      ? t('about.currentVersion', { version: normalizedServerVersion.value })
      : ''
  }
  return t('about.checkHint')
})
const desktopUpdateActionLoading = computed(() => (
  ['checking', 'downloading'].includes(desktopUpdate.value?.status ?? '')
))
const desktopUpdateActionDisabled = computed(() => desktopUpdateActionLoading.value)
const desktopUpdateActionLabel = computed(() => {
  switch (desktopUpdate.value?.status) {
    case 'checking':
      return t('about.checking')
    case 'available':
      return t('about.downloadUpdate')
    case 'downloading':
      return t('about.downloadingUpdate', { progress: desktopUpdate.value.progress ?? 0 })
    case 'downloaded':
      return t('about.restartToUpdate')
    case 'error':
      return t('about.retryUpdate')
    default:
      return t('about.checkForUpdates')
  }
})

const notesOpen = ref(false)
const advancedOpen = ref(false)

async function openServerConnection() {
  advancedOpen.value = false
  await router.push({
    name: 'ConnectServer',
    query: { returnTo: '/settings/about' },
  })
}

// Shorten GitHub issue / PR / commit URLs to compact references and drop the
// raw <samp> tags GitHub's generated notes leave behind (markstream renders
// them literally otherwise).
function cleanMarkdownBody(body: string): string {
  if (!body) return ''

  return body
    .replace(/<\/?samp>/gi, '')
    .replace(/(?:<)?(https:\/\/github\.com\/[^/]+\/[^/]+\/(?:issues|pull)\/(\d+))(?:>)?/g, (match, url, id, offset, str) => {
      if (str.substring(offset - 2, offset) === '](') return match
      return `[#${id}](${url})`
    })
    .replace(/(?:<)?(https:\/\/github\.com\/[^/]+\/[^/]+\/commit\/([a-f0-9]{7})[a-f0-9]*)(?:>)?/g, (match, url, hash, offset, str) => {
      if (str.substring(offset - 2, offset) === '](') return match
      return `[${hash}](${url})`
    })
}

onMounted(async () => {
  // Only resolve the current version/commit for display. Update checks are NOT
  // run here: browser checks stay manual and Electron main owns the native app
  // startup check.
  void capabilitiesStore.load()

  if (!desktopUpdates) return
  stopDesktopUpdateListener = desktopUpdates.onStateChanged((state) => {
    desktopUpdate.value = state
  })
  try {
    const [info, state] = await Promise.all([
      desktopUpdates.getInfo(),
      desktopUpdates.getState(),
    ])
    desktopUpdateInfo.value = info
    desktopUpdate.value = state
  } catch (error) {
    const reason = error instanceof Error ? error.message : String(error)
    toast.error(`${t('about.checkFailed')}: ${reason}`)
  }
})
onBeforeUnmount(() => stopDesktopUpdateListener?.())

async function recheck() {
  try {
    await update.check()
  } catch (error) {
    const reason = error instanceof Error ? error.message : String(error)
    toast.error(`${t('about.checkFailed')}: ${reason}`)
  }
}

async function runDesktopUpdateAction() {
  if (!desktopUpdates || desktopUpdateActionDisabled.value) return
  try {
    const status = desktopUpdate.value?.status
    const state = status === 'available'
      ? await desktopUpdates.download()
      : status === 'downloaded'
        ? await desktopUpdates.install()
        : await desktopUpdates.check()
    desktopUpdate.value = state
    if (state.status === 'error') {
      toast.error(`${t('about.checkFailed')}: ${state.error || t('about.desktopUpdateFailed')}`)
    }
  } catch (error) {
    const reason = error instanceof Error ? error.message : String(error)
    toast.error(`${t('about.checkFailed')}: ${reason}`)
  }
}
</script>

<style scoped>
/* Release notes render inside an opt-in dialog, so we only need to tame
   markstream-vue's animated link underline — no global tooltip machinery. */
.about-notes :deep(.markdown-renderer a.link-node) {
  --underline-iteration: 0;
  --underline-duration: 0s;
  text-decoration: underline;
  text-decoration-thickness: 1px;
  text-underline-offset: 3px;
  opacity: 0.85;
  transition: opacity 0.2s ease, color 0.2s ease;
}

.about-notes :deep(.markdown-renderer a.link-node:hover) {
  opacity: 1;
  color: var(--primary);
}
</style>
