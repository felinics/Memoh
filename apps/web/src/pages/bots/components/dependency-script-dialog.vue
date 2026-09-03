<script setup lang="ts">
// Read-only preview of the exact script a dependency action feeds the
// workspace shell (WD-API-001). Scripts never land on disk, so this is the one
// place a user can audit them. The caller owns the fetch (one query per
// action); when several actions are previewable a segmented switch asks the
// caller to load another one. Secret env values arrive blank from the Server
// and render as dots so the row still shows which variables are set.
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronRight } from 'lucide-vue-next'
import {
  Alert,
  AlertTitle,
  Button,
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
  Dialog,
  DialogBody,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
  InlineLoadingRow,
  ScrollArea,
  ScrollBar,
  SegmentedControl,
  TextButton,
  toast,
  useClipboard,
  type SegmentedItem,
} from '@felinic/ui'
import type { ScriptAction, ScriptResponse } from '@/composables/api/useWorkspaceDependencies'
import DependencyKvList, { type DependencyKvRow } from './dependency-kv-list.vue'

const props = withDefaults(defineProps<{
  open: boolean
  script?: ScriptResponse | null
  loading?: boolean
  /** Localized fetch failure. */
  error?: string
  dependencyName?: string
  /** The action currently previewed. */
  action?: ScriptAction
  /** Actions the caller can preview; a segmented switch appears for more than one. */
  actions?: ScriptAction[]
}>(), {
  script: null,
  loading: false,
  error: '',
  dependencyName: '',
  action: 'install',
  actions: () => [],
})

const emit = defineEmits<{
  'update:open': [value: boolean]
  'update:action': [value: ScriptAction]
}>()

const { t } = useI18n()
const { copyText } = useClipboard()

const envOpen = ref(false)

function actionLabel(action: ScriptAction): string {
  switch (action) {
    case 'update':
      return t('bots.dependencies.action.update')
    case 'remove':
      return t('bots.dependencies.action.remove')
    case 'reinstall':
      return t('bots.dependencies.action.reinstall')
    case 'rollback':
      return t('bots.dependencies.action.rollbackPlain')
    default:
      return t('bots.dependencies.action.install')
  }
}

const currentAction = computed<ScriptAction>(() => props.script?.action ?? props.action)
const segments = computed<SegmentedItem<ScriptAction>[]>(() =>
  props.actions.map(action => ({ value: action, label: actionLabel(action) })),
)

const description = computed(() => t('bots.dependencies.script.description', {
  name: props.dependencyName || props.script?.dependency_id || '',
  action: actionLabel(currentAction.value),
}))

const rows = computed<DependencyKvRow[]>(() => {
  const script = props.script
  if (!script) return []
  const timeout = script.timeout_seconds
  return [
    { label: t('bots.dependencies.script.digest'), value: script.digest, mono: true },
    { label: t('bots.dependencies.script.exec'), value: script.exec, mono: true },
    {
      label: t('bots.dependencies.script.timeout'),
      value: timeout ? t('bots.dependencies.script.timeoutValue', { seconds: timeout }) : '',
      mono: true,
    },
  ]
})

const env = computed(() => props.script?.env ?? [])

function envValue(entry: { value?: string; secret?: boolean }): string {
  return entry.secret ? '••••••••' : (entry.value ?? '')
}

async function copyScript() {
  const text = props.script?.script ?? ''
  if (!text) return
  const ok = await copyText(text)
  if (ok) toast.success(t('common.copied'))
  else toast.error(t('common.copyFailed'))
}
</script>

<template>
  <Dialog
    :open="open"
    @update:open="(value) => emit('update:open', value)"
  >
    <DialogPanel
      width="2xl"
      footer
    >
      <DialogHeader class="min-w-0">
        <DialogTitle class="break-words">
          {{ t('bots.dependencies.script.title') }}
        </DialogTitle>
        <DialogDescription class="break-words">
          {{ description }}
        </DialogDescription>
      </DialogHeader>

      <DialogBody class="min-w-0 space-y-4">
        <SegmentedControl
          v-if="segments.length > 1"
          :model-value="currentAction"
          :items="segments"
          :aria-label="t('bots.dependencies.script.title')"
          @update:model-value="(value) => emit('update:action', value)"
        />

        <InlineLoadingRow
          v-if="loading"
          size="sm"
        >
          {{ t('common.loading') }}
        </InlineLoadingRow>

        <Alert
          v-else-if="error"
          variant="destructive"
        >
          <AlertTitle>{{ error }}</AlertTitle>
        </Alert>

        <template v-else-if="script">
          <DependencyKvList :rows="rows" />

          <Collapsible
            v-if="env.length"
            v-model:open="envOpen"
          >
            <CollapsibleTrigger as-child>
              <TextButton>
                <ChevronRight
                  class="transition-transform"
                  :class="{ 'rotate-90': envOpen }"
                />
                {{ t('bots.dependencies.script.env', { count: env.length }) }}
              </TextButton>
            </CollapsibleTrigger>
            <CollapsibleContent>
              <div class="mt-2">
                <DependencyKvList :rows="env.map(entry => ({ label: entry.key ?? '', value: envValue(entry), mono: true }))" />
              </div>
            </CollapsibleContent>
          </Collapsible>

          <!-- Long script lines scroll sideways inside the box; the box itself
               never widens the dialog (min-w-0 on the grid rows above). -->
          <ScrollArea class="max-h-80 min-w-0 rounded-lg border border-border bg-muted-soft">
            <pre class="p-3 font-mono text-caption leading-relaxed whitespace-pre text-foreground">{{ script.script }}</pre>
            <ScrollBar orientation="horizontal" />
          </ScrollArea>
        </template>
      </DialogBody>

      <DialogFooter class="min-w-0">
        <Button
          variant="outline"
          @click="emit('update:open', false)"
        >
          {{ t('bots.dependencies.close') }}
        </Button>
        <Button
          :disabled="!script?.script"
          @click="copyScript"
        >
          {{ t('common.copy') }}
        </Button>
      </DialogFooter>
    </DialogPanel>
  </Dialog>
</template>
