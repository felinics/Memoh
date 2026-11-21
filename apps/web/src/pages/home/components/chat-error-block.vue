<script setup lang="ts">
import { Comment, computed, onBeforeUnmount, onMounted, ref, useSlots } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { Button, LabelSwap, useClipboard } from '@felinic/ui'
import { CircleAlert } from 'lucide-vue-next'
import { apiErrorCorrelation, parseMemohError } from '@/utils/api-error'

const props = defineProps<{
  message: string
  error?: unknown
  // The error arrived while the turn was on screen, not with loaded history.
  reveal?: boolean
}>()

const { t } = useI18n()
const router = useRouter()
const slots = useSlots()
const detailsOpen = ref(false)

// A live error replaces the thinking row seconds after send; appearing at full
// height reads as a jump. It unrolls top-down instead, the same grid 0fr→1fr
// technique and curve collapse-section uses, so the card's bottom edge moves
// down like a mask. Painted at 0fr for one frame first, or the height snaps.
const unrolled = ref(!props.reveal)
onMounted(() => {
  if (unrolled.value) return
  requestAnimationFrame(() => requestAnimationFrame(() => { unrolled.value = true }))
})

const PROVIDER_SETTINGS_CODES = new Set([
  'agent.provider_auth_failed',
  'agent.provider_permission_denied',
  'agent.provider_quota_exhausted',
  'agent.provider_request_rejected',
])

const CODE_TITLE_KEYS: Record<string, string> = {
  'agent.provider_quota_exhausted': 'chat.errorActions.titles.providerQuota',
  'agent.response_timeout': 'chat.errorActions.titles.timeout',
  'agent.tool_timeout': 'chat.errorActions.titles.timeout',
  'agent.provider_unreachable': 'chat.errorActions.titles.unreachable',
  'agent.provider_overloaded': 'chat.errorActions.titles.overloaded',
  'agent.provider_rate_limited': 'chat.errorActions.titles.overloaded',
  'agent.provider_auth_failed': 'chat.errorActions.titles.auth',
  'agent.provider_permission_denied': 'chat.errorActions.titles.auth',
  'agent.provider_request_rejected': 'chat.errorActions.titles.rejected',
  'agent.response_interrupted': 'chat.errorActions.titles.interrupted',
  'external_runtime.auth_required': 'chat.errorActions.titles.agentAuth',
}

function hasCustomAction() {
  return Boolean(slots.default?.().some(node => node.type !== Comment))
}

const errorCode = computed(() => {
  const direct = (props.error as { code?: unknown } | undefined)?.code
  if (typeof direct === 'string' && direct.trim()) return direct.trim()
  return parseMemohError(props.error)?.code ?? ''
})

const errorArgs = computed<Record<string, unknown> | undefined>(() => {
  const direct = (props.error as { args?: unknown } | undefined)?.args
  if (direct && typeof direct === 'object' && !Array.isArray(direct)) {
    return direct as Record<string, unknown>
  }
  return parseMemohError(props.error)?.args
})

const upstreamText = computed(() => {
  const raw = errorArgs.value?.upstream
  return typeof raw === 'string' ? raw.trim() : ''
})

const isProviderSettingsError = computed(() =>
  PROVIDER_SETTINGS_CODES.has(errorCode.value),
)

const titleKey = computed(() => {
  const code = errorCode.value
  if (CODE_TITLE_KEYS[code]) return CODE_TITLE_KEYS[code]
  if (code.startsWith('agent_credential.') || code.startsWith('agent_authorization.')) {
    return 'chat.errorActions.titles.agentAuth'
  }
  return 'chat.errorActions.titles.default'
})

const title = computed(() => t(titleKey.value))

// Diagnostics needed to trace a failure in logs: the rendered message plus any
// correlated request/trace identifiers.
const correlation = computed(() => apiErrorCorrelation(props.error))
const correlationText = computed(() => {
  const { requestId, traceId } = correlation.value
  if (requestId) return `${t('common.requestId')}: ${requestId}`
  return traceId ? `${t('common.traceId')}: ${traceId}` : ''
})
const showDetailsToggle = computed(() => Boolean(upstreamText.value || correlationText.value))

const report = computed(() => {
  const { requestId, traceId } = correlation.value
  return [
    props.message,
    upstreamText.value,
    requestId ? `${t('common.requestId')}: ${requestId}` : '',
    traceId ? `${t('common.traceId')}: ${traceId}` : '',
  ].filter(Boolean).join('\n')
})

const copyStatus = ref<'idle' | 'success' | 'failure'>('idle')

let resetTimer: ReturnType<typeof setTimeout> | undefined
onBeforeUnmount(() => clearTimeout(resetTimer))
async function copyReport() {
  clearTimeout(resetTimer)
  const copied = await useClipboard().copyText(report.value)
  copyStatus.value = copied ? 'success' : 'failure'
  resetTimer = setTimeout(() => { copyStatus.value = 'idle' }, 1500)
}

function openProviders() {
  void router.push('/settings/providers')
}

function hasPrimaryAction() {
  return hasCustomAction() || isProviderSettingsError.value
}
</script>

<template>
  <div
    class="grid transition-[grid-template-rows] duration-[400ms] ease-[cubic-bezier(0.16,1,0.3,1)] motion-reduce:transition-none"
    :class="unrolled ? 'grid-rows-[1fr]' : 'grid-rows-[0fr]'"
  >
    <div class="min-h-0 overflow-hidden">
      <div
        role="alert"
        class="chat-error-block w-full max-w-120 overflow-hidden rounded-2xl border border-border bg-card dark:border-transparent"
      >
        <div class="px-5 py-4">
          <div class="flex items-start gap-2 text-[0.9375rem] leading-[1.375rem] tracking-[-0.01em]">
            <span class="flex h-lh shrink-0 items-center text-destructive">
              <CircleAlert
                class="size-4.5"
                :stroke-width="1.75"
                aria-hidden="true"
              />
            </span>
            <span class="min-w-0 font-semibold text-foreground">{{ title }}</span>
          </div>

          <p class="mt-1.5 break-words whitespace-pre-wrap text-label leading-5 font-normal text-muted-foreground">
            {{ message }}
          </p>

          <div class="mt-3.5 flex flex-wrap items-center justify-between gap-2">
            <div class="flex flex-wrap items-center gap-1.5">
              <slot v-if="hasCustomAction()" />
              <Button
                v-else-if="isProviderSettingsError"
                type="button"
                size="sm"
                variant="secondary"
                @click="openProviders"
              >
                {{ t('chat.errorActions.providerSettings') }}
              </Button>
              <Button
                type="button"
                size="sm"
                :variant="hasPrimaryAction() ? 'ghost' : 'secondary'"
                :tone="hasPrimaryAction() ? 'muted' : undefined"
                @click="copyReport"
              >
                <LabelSwap :active="copyStatus">
                  <template #idle>
                    {{ t('chat.errorActions.copy') }}
                  </template>
                  <template #success>
                    {{ t('chat.errorActions.copied') }}
                  </template>
                  <template #failure>
                    {{ t('common.copyFailed') }}
                  </template>
                </LabelSwap>
              </Button>
            </div>

            <Button
              v-if="showDetailsToggle"
              type="button"
              variant="link"
              class="mt-[0.1875rem] font-normal"
              :aria-expanded="detailsOpen"
              @click="detailsOpen = !detailsOpen"
            >
              {{ t(detailsOpen ? 'chat.errorActions.hideDetails' : 'chat.errorActions.showDetails') }}
            </Button>
          </div>

          <div
            v-if="showDetailsToggle"
            class="grid transition-[grid-template-rows] duration-200 ease-out"
            :class="detailsOpen ? 'grid-rows-[1fr]' : 'grid-rows-[0fr]'"
          >
            <div class="min-h-0 overflow-hidden">
              <div class="mt-3 space-y-1.5 border-t border-border-soft pt-3">
                <p
                  v-if="upstreamText"
                  class="select-text break-words whitespace-pre-wrap font-mono text-body leading-relaxed text-muted-foreground"
                >
                  {{ upstreamText }}
                </p>
                <p
                  v-if="correlationText"
                  class="select-text break-all text-body tabular-nums text-muted-foreground"
                >
                  {{ correlationText }}
                </p>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
