<template>
  <!-- Update affordance: a right-anchored circle that grows LEFT into a text
       pill on hover/focus, eating the user block's flex slack (it is min-w-0,
       so no overlap — the pointer can only be in one place at a time).
       Idle/up-to-date/checking render nothing: the only states worth chrome
       are the ones the user can act on. The icon is an absolute overlay that
       fades out as the label's grid track opens (0fr → 1fr), so the expanded
       pill is text-only and the whole morph is one grid tween with constant horizontal padding
       (equal to the circle diameter, so min-width cannot cut the tween short) — no JS expand state. -->
  <button
    v-if="bridge && status"
    type="button"
    class="group relative flex h-6 min-w-6 shrink-0 animate-in fade-in zoom-in-90 cursor-pointer select-none items-center rounded-full px-3 outline-none motion-reduce:animate-none motion-reduce:transition-none transition-[background-color,color] duration-[180ms] ease-[cubic-bezier(0.22,1,0.36,1)] focus-visible:ring-2 focus-visible:ring-ring"
    :class="toneClass"
    :aria-label="accessibleLabel"
    :disabled="pending || status === 'installing'"
    @click="onClick"
  >
    <span
      class="grid motion-reduce:transition-none transition-[grid-template-columns] duration-[180ms] ease-[cubic-bezier(0.22,1,0.36,1)] grid-cols-[0fr] group-hover:grid-cols-[1fr] group-focus-visible:grid-cols-[1fr]"
    >
      <span class="min-w-0 overflow-hidden">
        <span class="whitespace-nowrap text-label font-medium">{{ label }}</span>
      </span>
    </span>
    <span
      class="pointer-events-none absolute inset-0 flex items-center justify-center transition-opacity duration-[180ms] ease-[cubic-bezier(0.22,1,0.36,1)] motion-reduce:transition-none group-hover:opacity-0 group-focus-visible:opacity-0"
    >
      <TriangleAlert
        v-if="status === 'error'"
        :stroke-width="1.75"
        class="size-3"
      />
      <UpdateIcon
        v-else
        class="size-3"
        aria-hidden="true"
      />
    </span>
  </button>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { TriangleAlert } from 'lucide-vue-next'
import { UpdateIcon } from '@memohai/icon/ui'
import type { DesktopUpdateStatus } from '@/lib/desktop-shell'
import { useDesktopUpdates } from '@/composables/useDesktopUpdates'

const router = useRouter()
const { t } = useI18n()
const { bridge, state } = useDesktopUpdates()
const pending = ref(false)

type VisibleStatus = Extract<DesktopUpdateStatus, 'downloading' | 'downloaded' | 'installing' | 'error'>
const VISIBLE: VisibleStatus[] = ['downloading', 'downloaded', 'installing', 'error']

const status = computed<VisibleStatus | null>(() => {
  const s = state.value?.status
  return VISIBLE.includes(s as VisibleStatus) ? (s as VisibleStatus) : null
})

const accessibleLabel = computed(() => {
  switch (status.value) {
    case 'installing':
      return t('about.installingUpdate')
    case 'downloading':
      return state.value?.progress == null ? t('about.preparingUpdate') : t('about.downloadingUpdate', { progress: state.value.progress })
    case 'downloaded':
      return t('about.restartToUpdate')
    case 'error':
      return t('about.desktopUpdateFailed')
    default:
      return ''
  }
})

const label = computed(() => {
  switch (status.value) {
    case 'installing':
      return t('sidebar.updateChip.updating')
    case 'downloading':
      return state.value?.progress == null ? t('sidebar.updateChip.updating') : `${Math.round(state.value.progress)}%`
    case 'downloaded':
      return t('sidebar.updateChip.restart')
    case 'error':
      return t('sidebar.updateChip.error')
    default:
      return ''
  }
})

const toneClass = computed(() => status.value === 'error'
  ? 'bg-accent-gray-soft-active text-accent-gray-deep'
  : 'bg-[color:var(--brand)] text-[color:var(--brand-foreground)] hover:bg-[color:var(--brand-hover)]') /* ui-allow-style: circle-to-pill update affordance owns its theme fill */

async function onClick() {
  const current = bridge.value
  if (!current || pending.value || status.value === 'installing') return
  pending.value = true
  try {
    if (status.value === 'downloaded') {
      await current.install()
    } else {
      // downloading / error: details and retry live on the About page.
      await router.push({ name: 'about', query: { updates: '1' } })
    }
  } catch (error) {
    console.warn('update chip: action failed', error)
  } finally {
    pending.value = false
  }
}
</script>
