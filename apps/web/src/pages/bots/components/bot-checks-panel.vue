<template>
  <!-- Diagnostics live behind this dialog now, not on Overview. It only opens
       from the Overview issue banner (or any explicit trigger), so the runtime
       check list — formerly the page's centerpiece — is out of the way until a
       bot actually needs attention. Reloads its checks each time it opens. -->
  <Dialog
    :open="open"
    @update:open="(v) => emit('update:open', v)"
  >
    <DialogContent class="flex max-h-[80vh] w-full max-w-xl flex-col gap-0 overflow-hidden p-0 sm:max-w-xl">
      <DialogHeader class="border-b border-border px-6 py-4 text-left">
        <DialogTitle>{{ $t('bots.checks.diagnosticTitle') }}</DialogTitle>
        <DialogDescription>{{ $t('bots.checks.diagnosticSubtitle') }}</DialogDescription>
      </DialogHeader>

      <div
        v-if="checks.length > 0"
        class="flex items-center justify-end gap-1 border-b border-border px-4 py-1.5"
      >
        <Button
          variant="ghost"
          size="sm"
          class="h-7 px-2 text-xs text-muted-foreground hover:text-foreground"
          @click="toggleAll(true)"
        >
          {{ $t('bots.checks.expandAll') }}
        </Button>
        <Button
          variant="ghost"
          size="sm"
          class="h-7 px-2 text-xs text-muted-foreground hover:text-foreground"
          @click="toggleAll(false)"
        >
          {{ $t('bots.checks.collapse') }}
        </Button>
      </div>

      <div class="min-h-0 flex-1 overflow-y-auto px-4 py-4">
        <!-- Loading -->
        <div
          v-if="checksLoading && checks.length === 0"
          class="space-y-2"
        >
          <div
            v-for="i in 5"
            :key="i"
            class="rounded-md border px-3 opacity-60"
          >
            <div class="flex items-center gap-3 py-2">
              <Skeleton class="h-4 w-full" />
            </div>
          </div>
        </div>

        <!-- Empty -->
        <Empty
          v-else-if="checks.length === 0"
          class="py-10"
        >
          <EmptyTitle>{{ $t('bots.checks.empty') }}</EmptyTitle>
        </Empty>

        <!-- Check list -->
        <div
          v-else
          class="space-y-2"
        >
          <Collapsible
            v-for="item in checks"
            :key="item.id"
            :open="expandedIds.has(item.id!)"
            class="rounded-md border transition-opacity"
            :class="item.status === 'ok' ? 'opacity-60 hover:opacity-100' : 'opacity-100'"
          >
            <CollapsibleTrigger
              class="flex w-full items-center justify-between gap-3 px-3 py-2 text-left hover:bg-accent/40"
              @click="toggleItem(item.id!)"
            >
              <div class="flex min-w-0 items-center gap-3">
                <component
                  :is="getStatusIcon(item.status)"
                  :class="['size-4 shrink-0', getStatusColor(item.status)]"
                />
                <div class="min-w-0 space-y-0.5">
                  <p class="truncate text-xs font-medium leading-none">
                    {{ checkTitleLabel(item) }}
                  </p>
                  <p
                    v-if="item.subtitle"
                    class="truncate text-[10px] text-muted-foreground"
                  >
                    {{ item.subtitle }}
                  </p>
                </div>
              </div>
              <ChevronRight
                class="size-4 shrink-0 text-muted-foreground transition-transform duration-200"
                :class="{ 'rotate-90': expandedIds.has(item.id!) }"
              />
            </CollapsibleTrigger>

            <CollapsibleContent>
              <div class="space-y-3 border-t px-3 py-3">
                <p class="text-xs leading-relaxed text-foreground">
                  {{ item.summary }}
                </p>

                <div
                  v-if="item.detail"
                  class="group/code relative rounded border bg-muted/30"
                >
                  <div class="absolute right-1.5 top-1.5 opacity-0 transition-opacity group-hover/code:opacity-100">
                    <Button
                      variant="ghost"
                      size="icon"
                      class="size-6"
                      :aria-label="$t('common.copy')"
                      @click.stop="copyToClipboard(item.detail)"
                    >
                      <Copy class="size-3" />
                    </Button>
                  </div>
                  <pre class="max-h-[240px] select-text overflow-x-auto overflow-y-auto whitespace-pre-wrap p-3 font-mono text-[11px] leading-relaxed"><code>{{ item.detail }}</code></pre>
                </div>
              </div>
            </CollapsibleContent>
          </Collapsible>
        </div>
      </div>
    </DialogContent>
  </Dialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { getBotsByIdChecks, type BotsBotCheck } from '@memohai/sdk'
import { useI18n } from 'vue-i18n'
import {
  Button,
  Collapsible,
  CollapsibleTrigger,
  CollapsibleContent,
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  Empty,
  EmptyTitle,
  Skeleton,
  toast,
} from '@felinic/ui'
import {
  CheckCircle2,
  AlertTriangle,
  XCircle,
  ChevronRight,
  Copy,
  HelpCircle,
} from 'lucide-vue-next'
import { useClipboard } from '@felinic/ui'
import { resolveApiErrorMessage } from '@/utils/api-error'

type BotCheck = BotsBotCheck

const props = defineProps<{
  botId: string
  open: boolean
}>()

const emit = defineEmits<{
  'update:open': [value: boolean]
}>()

const { t } = useI18n()
const { copyText } = useClipboard()

const checks = ref<BotCheck[]>([])
const checksLoading = ref(false)
const expandedIds = ref<Set<string>>(new Set())

// Reload checks every time the dialog opens, so a user re-checking after a fix
// always sees fresh state. Abnormal items expand by default.
watch(
  () => props.open,
  (isOpen) => {
    if (isOpen && props.botId) void loadChecks()
  },
)

async function loadChecks() {
  checksLoading.value = true
  try {
    const { data } = await getBotsByIdChecks({ path: { id: props.botId }, throwOnError: true })
    checks.value = data?.items ?? []
    expandedIds.value = new Set(
      checks.value.filter((c) => c.status !== 'ok').map((c) => c.id!),
    )
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.checks.loadFailed')))
  } finally {
    checksLoading.value = false
  }
}

function toggleItem(id: string) {
  if (expandedIds.value.has(id)) {
    expandedIds.value.delete(id)
  } else {
    expandedIds.value.add(id)
  }
}

function toggleAll(expand: boolean) {
  expandedIds.value = expand
    ? new Set(checks.value.map((c) => c.id!))
    : new Set()
}

async function copyToClipboard(text: string) {
  const ok = await copyText(text)
  if (!ok) {
    toast.error(t('common.copyFailed'))
    return
  }
  toast.success(t('common.copied'))
}

function getStatusIcon(status: BotCheck['status']) {
  if (status === 'error') return XCircle
  if (status === 'warn') return AlertTriangle
  if (status === 'ok') return CheckCircle2
  return HelpCircle
}

function getStatusColor(status: BotCheck['status']) {
  if (status === 'error') return 'text-destructive'
  if (status === 'warn') return 'text-warning'
  if (status === 'ok') return 'text-foreground/40'
  return 'text-muted-foreground'
}

// Prefer the server-provided i18n title key; fall back to type/id when missing.
function checkTitleLabel(item: BotCheck): string {
  const titleKey = (item.title_key ?? '').trim()
  if (titleKey) {
    const translated = t(titleKey)
    if (translated !== titleKey) return translated
  }
  return (item.type ?? '').trim() || (item.id ?? '').trim() || '-'
}
</script>
