<script setup lang="ts">
import { computed, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, toast, useClipboard } from '@felinic/ui'
import { Check, Copy, Download } from 'lucide-vue-next'
import type { HandlersContextTrajectoryBlock } from '@memohai/sdk'

const props = defineProps<{ block: HandlersContextTrajectoryBlock, label: string }>()
const { t, locale } = useI18n()
const { copyText } = useClipboard()
const expanded = shallowRef(false)
const copied = shallowRef(false)
const content = computed(() => props.block.content ?? '')
const long = computed(() => content.value.length > 4096)
const preview = computed(() => expanded.value ? content.value : content.value.slice(0, 4096))
const bytes = computed(() => new Intl.NumberFormat(locale.value).format(props.block.bytes ?? 0))
watch(() => props.block, () => { expanded.value = false; copied.value = false })

async function copy() {
  const block = props.block
  const ok = await copyText(content.value)
  if (props.block !== block) return
  if (ok) copied.value = true
  else toast.error(t('common.copyFailed'))
}

function download() {
  try {
    const binary = props.block.encoding === 'base64'
    const blob = binary
      ? new Blob([Uint8Array.from(atob(content.value), char => char.charCodeAt(0))])
      : new Blob([content.value], { type: 'text/plain;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `trajectory-${props.block.hash ?? 'content'}.${binary ? 'bin' : props.block.format === 'json' ? 'json' : 'txt'}`
    document.body.append(link)
    link.click()
    link.remove()
    setTimeout(() => URL.revokeObjectURL(url), 0)
  } catch {
    toast.error(t('chat.trajectory.captureDownloadFailed'))
  }
}
</script>

<template>
  <div class="min-w-0 space-y-1">
    <div class="flex items-center gap-1">
      <span class="min-w-0 flex-1 text-caption text-muted-foreground">
        {{ label }} · {{ $t('chat.trajectory.captureBytes', { n: bytes }) }}
      </span>
      <template v-if="block.available">
        <Button
          variant="ghost"
          size="icon-sm"
          :aria-label="$t('chat.trajectory.captureCopy')"
          data-testid="capture-copy"
          @click="copy"
        >
          <Check v-if="copied" /><Copy v-else />
        </Button>
        <Button
          variant="ghost"
          size="icon-sm"
          :aria-label="$t('chat.trajectory.captureDownload')"
          data-testid="capture-download"
          @click="download"
        >
          <Download />
        </Button>
      </template>
    </div>
    <p
      v-if="!block.available"
      class="text-caption text-warning"
    >
      {{ $t('chat.trajectory.captureUnavailable') }}
    </p>
    <template v-else>
      <p
        v-if="block.encoding === 'base64'"
        class="text-caption text-muted-foreground"
      >
        {{ $t('chat.trajectory.captureBase64') }}
      </p>
      <pre
        v-if="content"
        tabindex="0"
        :aria-label="label"
        class="max-h-96 overflow-auto whitespace-pre-wrap break-all font-mono text-body"
      >{{ preview }}</pre>
      <p
        v-else
        class="text-caption text-muted-foreground"
      >
        {{ $t('chat.trajectory.captureEmpty') }}
      </p>
      <Button
        v-if="long"
        variant="ghost"
        size="sm"
        :aria-expanded="expanded"
        data-testid="capture-expand"
        @click="expanded = !expanded"
      >
        {{ $t(expanded ? 'chat.trajectory.captureCollapse' : 'chat.trajectory.captureExpand') }}
      </Button>
    </template>
  </div>
</template>
