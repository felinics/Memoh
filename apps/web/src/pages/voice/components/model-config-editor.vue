<template>
  <div class="space-y-4">
    <ModelConfigFields
      v-if="basicFields.length > 0"
      :fields="basicFields"
      :values="configData"
      :id-prefix="`${mode}-field`"
      @update:field="setConfigField"
    />

    <div
      v-if="basicFields.length === 0 && advancedFields.length === 0"
      class="text-xs text-muted-foreground"
    >
      {{ mode === 'transcription' ? $t('transcription.noCapabilities') : $t('speech.noCapabilities') }}
    </div>

    <!-- Advanced is a quiet disclosure, not a bordered sub-card: the same
         chevron toggle + height reveal the schedule "More options" uses, so the
         rarely-touched vendor knobs stay folded away until the user asks. -->
    <div v-if="advancedFields.length > 0">
      <TextButton
        type="button"
        class="gap-1.5 text-xs"
        @click="showAdvanced = !showAdvanced"
      >
        <ChevronRight
          class="size-3.5 transition-transform"
          :class="showAdvanced ? 'rotate-90' : ''"
        />
        {{ mode === 'transcription' ? $t('transcription.advanced.title') : $t('speech.advanced.title') }}
      </TextButton>

      <div
        class="grid overflow-hidden transition-[grid-template-rows] duration-200 ease-out"
        :class="showAdvanced ? 'grid-rows-[1fr]' : 'grid-rows-[0fr]'"
      >
        <div class="min-h-0">
          <div class="mt-3 space-y-4">
            <p class="text-xs text-muted-foreground">
              {{ mode === 'transcription' ? $t('transcription.advanced.description') : $t('speech.advanced.description') }}
            </p>
            <ModelConfigFields
              :fields="advancedFields"
              :values="configData"
              :id-prefix="`${mode}-advanced-field`"
              @update:field="setConfigField"
            />
          </div>
        </div>
      </div>
    </div>

    <div class="space-y-3">
      <Label>
        {{ mode === 'transcription' ? $t('transcription.test.title') : $t('speech.test.title') }}
      </Label>
      <div
        v-if="mode === 'synthesis'"
        class="relative"
      >
        <Textarea
          v-model="testText"
          :placeholder="$t('speech.test.placeholder')"
          :maxlength="maxTestTextLen"
          rows="2"
          class="resize-none"
        />
        <span class="absolute right-2 bottom-2 text-xs text-muted-foreground">
          {{ testText.length }}/{{ maxTestTextLen }}
        </span>
      </div>
      <div
        v-else
        class="space-y-2"
      >
        <Input
          type="file"
          accept="audio/*"
          class="file:-mt-1"
          @change="handleFileChange"
        />
        <p
          v-if="selectedFileName"
          class="text-xs text-muted-foreground"
        >
          {{ selectedFileName }}
        </p>
      </div>
      <div class="flex items-center gap-3">
        <LoadingButton
          type="button"
          variant="outline"
          size="sm"
          :loading="testLoading"
          :disabled="mode === 'synthesis' ? (!testText.trim() || testText.length > maxTestTextLen) : !selectedFile"
          @click="handleTest"
        >
          <Play
            v-if="mode === 'synthesis'"
            class="mr-1.5"
          />
          {{ mode === 'transcription' ? $t('transcription.test.run') : $t('speech.test.generate') }}
        </LoadingButton>
        <span
          v-if="testError"
          class="text-xs text-destructive"
        >
          {{ testError }}
        </span>
      </div>
      <div
        v-if="mode === 'synthesis' && audioUrl"
        class="rounded-md border border-border bg-muted p-3"
      >
        <audio
          ref="audioEl"
          :src="audioUrl"
          controls
          class="w-full"
        />
      </div>
      <div
        v-if="mode === 'transcription' && transcriptionText"
        class="rounded-md border border-border bg-muted p-3 space-y-2"
      >
        <p class="text-sm whitespace-pre-wrap wrap-break-word">
          {{ transcriptionText }}
        </p>
        <p
          v-if="transcriptionLanguage"
          class="text-xs text-muted-foreground"
        >
          {{ transcriptionLanguage }}
        </p>
      </div>
    </div>

    <DialogFooter>
      <LoadingButton
        type="button"
        :loading="saving"
        @click="handleSaveConfig"
      >
        {{ $t('provider.saveChanges') }}
      </LoadingButton>
    </DialogFooter>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  DialogFooter,
  Input,
  Label,
  TextButton,
  Textarea,
  toast,
} from '@felinic/ui'
import type { AudioConfigSchema } from '@memohai/sdk'
import { ChevronRight, Play } from 'lucide-vue-next'
import LoadingButton from '@/components/loading-button/index.vue'
import ModelConfigFields, { type AudioFieldWithKey } from './model-config-fields.vue'
import { resolveApiErrorMessage } from '@/utils/api-error'

const props = defineProps<{
  modelId: string
  config: Record<string, unknown>
  schema: AudioConfigSchema | null
  mode?: 'synthesis' | 'transcription'
  onTest: (payload: string | File, config: Record<string, unknown>) => Promise<Blob | { text?: string, language?: string }>
  onSave?: (config: Record<string, unknown>) => Promise<unknown> | unknown
}>()

const { t } = useI18n()
const configData = reactive<Record<string, unknown>>({})
const saving = ref(false)
const showAdvanced = ref(false)
const testText = ref('')
const selectedFile = ref<File | null>(null)
const selectedFileName = ref('')
const testLoading = ref(false)
const testError = ref('')
const audioUrl = ref('')
const transcriptionText = ref('')
const transcriptionLanguage = ref('')
const audioEl = ref<HTMLAudioElement>()
const maxTestTextLen = 500
const mode = computed(() => props.mode ?? 'synthesis')

const orderedFields = computed<AudioFieldWithKey[]>(() => {
  const fields = (props.schema?.fields ?? []).filter((field): field is AudioFieldWithKey =>
    typeof field.key === 'string' && field.key.length > 0,
  )
  return [...fields].sort((a, b) => (a.order ?? 0) - (b.order ?? 0))
})

const basicFields = computed(() => orderedFields.value.filter(field => !field.advanced))
const advancedFields = computed(() => orderedFields.value.filter(field => field.advanced))

function setConfigField(key: string, value: unknown) {
  configData[key] = value
}

watch(() => props.config, (cfg) => {
  Object.keys(configData).forEach((key) => delete configData[key])
  Object.assign(configData, { ...(cfg ?? {}) })
  showAdvanced.value = advancedFields.value.some((field) => {
    const value = cfg?.[field.key]
    return value !== '' && value != null
  })
}, { immediate: true, deep: true })

function buildConfig(): Record<string, unknown> {
  const result: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(configData)) {
    if (value === '' || value == null) continue
    result[key] = value
  }
  return result
}

function revokeAudio() {
  if (audioUrl.value) {
    URL.revokeObjectURL(audioUrl.value)
    audioUrl.value = ''
  }
}

function resetTranscription() {
  transcriptionText.value = ''
  transcriptionLanguage.value = ''
}

let disposed = false
onBeforeUnmount(() => {
  disposed = true
  revokeAudio()
})

async function handleSaveConfig() {
  if (saving.value) return
  saving.value = true
  try {
    await props.onSave?.(buildConfig())
  } finally {
    saving.value = false
  }
}

async function handleTest() {
  if (mode.value === 'synthesis' && !testText.value.trim()) return
  if (mode.value === 'transcription' && !selectedFile.value) return
  testLoading.value = true
  testError.value = ''
  revokeAudio()
  resetTranscription()

  try {
    const result = await props.onTest(mode.value === 'synthesis' ? testText.value : selectedFile.value as File, buildConfig())
    if (disposed) return

    if (mode.value === 'synthesis') {
      const blob = result as Blob
      audioUrl.value = URL.createObjectURL(blob)
      await new Promise<void>((resolve) => setTimeout(resolve, 50))
      if (!disposed) audioEl.value?.play()
    } else {
      const payload = result as { text?: string, language?: string }
      transcriptionText.value = payload.text ?? ''
      transcriptionLanguage.value = payload.language ?? ''
    }
  } catch (error: unknown) {
    if (disposed) return
    const msg = resolveApiErrorMessage(error, t(mode.value === 'transcription' ? 'transcription.test.failed' : 'speech.test.failed'))
    testError.value = msg
    if (msg) toast.error(msg)
  } finally {
    testLoading.value = false
  }
}

function handleFileChange(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0] ?? null
  selectedFile.value = file
  selectedFileName.value = file?.name ?? ''
}
</script>
