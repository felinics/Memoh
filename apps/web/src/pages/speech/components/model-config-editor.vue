<template>
  <div class="space-y-4">
    <div
      v-if="basicFields.length > 0"
      class="grid gap-4 md:grid-cols-2"
    >
      <FieldStack
        v-for="field in basicFields"
        :key="field.key"
        :label="field.title || field.key"
        :for="field.type === 'bool' || field.type === 'enum' ? undefined : `tts-field-${field.key}`"
        :help="field.description"
        :class="isWideField(field) ? 'md:col-span-2' : ''"
      >
        <div
          v-if="field.type === 'secret'"
          class="relative"
        >
          <Input
            :id="`tts-field-${field.key}`"
            v-model="configData[field.key] as string"
            :type="visibleSecrets[field.key] ? 'text' : 'password'"
            :placeholder="field.example ? String(field.example) : ''"
          />
          <button
            type="button"
            class="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
            @click="visibleSecrets[field.key] = !visibleSecrets[field.key]"
          >
            <component
              :is="visibleSecrets[field.key] ? EyeOff : Eye"
              class="size-3.5"
            />
          </button>
        </div>

        <Switch
          v-else-if="field.type === 'bool'"
          :model-value="!!configData[field.key]"
          @update:model-value="(val) => configData[field.key] = !!val"
        />

        <Input
          v-else-if="field.type === 'number'"
          :id="`tts-field-${field.key}`"
          v-model.number="configData[field.key] as number"
          type="number"
          :placeholder="field.example ? String(field.example) : ''"
        />

        <SearchableSelectPopover
          v-else-if="field.type === 'enum' && field.enum"
          :model-value="stringConfigValue(field.key)"
          :options="enumOptions(field)"
          :placeholder="enumPlaceholder(field)"
          :aria-label="field.title || field.key"
          :search-placeholder="t('common.search')"
          :search-aria-label="t('common.search')"
          :empty-text="t('common.noData')"
          :show-group-headers="false"
          popover-class="w-[var(--reka-popover-trigger-width)]"
          @update:model-value="(val) => configData[field.key] = val"
        />

        <Input
          v-else
          :id="`tts-field-${field.key}`"
          v-model="configData[field.key] as string"
          type="text"
          :placeholder="field.example ? String(field.example) : ''"
        />
      </FieldStack>
    </div>

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
      <button
        type="button"
        class="flex items-center gap-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground"
        @click="showAdvanced = !showAdvanced"
      >
        <ChevronRight
          class="size-3.5"
          :class="showAdvanced ? 'rotate-90' : ''"
        />
        {{ mode === 'transcription' ? $t('transcription.advanced.title') : $t('speech.advanced.title') }}
      </button>

      <div
        class="grid overflow-hidden transition-[grid-template-rows] duration-200 ease-out"
        :class="showAdvanced ? 'grid-rows-[1fr]' : 'grid-rows-[0fr]'"
      >
        <div class="min-h-0">
          <div class="mt-3 space-y-4">
            <p class="text-xs text-muted-foreground">
              {{ mode === 'transcription' ? $t('transcription.advanced.description') : $t('speech.advanced.description') }}
            </p>
            <div class="grid gap-4 md:grid-cols-2">
              <FieldStack
                v-for="field in advancedFields"
                :key="field.key"
                :label="field.title || field.key"
                :for="field.type === 'bool' || field.type === 'enum' ? undefined : `tts-field-${field.key}`"
                :help="field.description"
                :class="isWideField(field) ? 'md:col-span-2' : ''"
              >
                <div
                  v-if="field.type === 'secret'"
                  class="relative"
                >
                  <Input
                    :id="`tts-field-${field.key}`"
                    v-model="configData[field.key] as string"
                    :type="visibleSecrets[field.key] ? 'text' : 'password'"
                    :placeholder="field.example ? String(field.example) : ''"
                  />
                  <button
                    type="button"
                    class="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                    @click="visibleSecrets[field.key] = !visibleSecrets[field.key]"
                  >
                    <component
                      :is="visibleSecrets[field.key] ? EyeOff : Eye"
                      class="size-3.5"
                    />
                  </button>
                </div>

                <Switch
                  v-else-if="field.type === 'bool'"
                  :model-value="!!configData[field.key]"
                  @update:model-value="(val) => configData[field.key] = !!val"
                />

                <Input
                  v-else-if="field.type === 'number'"
                  :id="`tts-field-${field.key}`"
                  v-model.number="configData[field.key] as number"
                  type="number"
                  :placeholder="field.example ? String(field.example) : ''"
                />

                <SearchableSelectPopover
                  v-else-if="field.type === 'enum' && field.enum"
                  :model-value="stringConfigValue(field.key)"
                  :options="enumOptions(field)"
                  :placeholder="enumPlaceholder(field)"
                  :aria-label="field.title || field.key"
                  :search-placeholder="t('common.search')"
                  :search-aria-label="t('common.search')"
                  :empty-text="t('common.noData')"
                  :show-group-headers="false"
                  popover-class="w-[var(--reka-popover-trigger-width)]"
                  @update:model-value="(val) => configData[field.key] = val"
                />

                <Input
                  v-else
                  :id="`tts-field-${field.key}`"
                  v-model="configData[field.key] as string"
                  type="text"
                  :placeholder="field.example ? String(field.example) : ''"
                />
              </FieldStack>
            </div>
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
        class="rounded-md border border-border bg-muted/30 p-3"
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
        class="rounded-md border border-border bg-muted/30 p-3 space-y-2"
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
import {
  DialogFooter,
  Input,
  Label,
  Switch,
  Textarea,
} from '@felinic/ui'
import { ChevronRight, Eye, EyeOff, Play } from 'lucide-vue-next'
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { FieldStack, toast } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import LoadingButton from '@/components/loading-button/index.vue'
import SearchableSelectPopover from '@/components/searchable-select-popover/index.vue'
import type { SearchableSelectOption } from '@/components/searchable-select-popover/index.vue'

interface SpeechFieldSchema {
  key: string
  type: string
  title?: string
  description?: string
  required?: boolean
  advanced?: boolean
  enum?: string[]
  example?: unknown
  order?: number
}

interface SpeechConfigSchema {
  fields?: SpeechFieldSchema[]
}

const props = defineProps<{
  modelId: string
  modelName: string
  config: Record<string, unknown>
  schema: SpeechConfigSchema | null
  mode?: 'synthesis' | 'transcription'
  onTest: (payload: string | File, config: Record<string, unknown>) => Promise<Blob | { text?: string, language?: string }>
}>()

const emit = defineEmits<{
  save: [config: Record<string, unknown>]
}>()

const { t } = useI18n()
const configData = reactive<Record<string, unknown>>({})
const visibleSecrets = reactive<Record<string, boolean>>({})
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

const orderedFields = computed(() => {
  const fields = props.schema?.fields ?? []
  return [...fields].sort((a, b) => (a.order ?? 0) - (b.order ?? 0))
})

const basicFields = computed(() => orderedFields.value.filter(field => !field.advanced))
const advancedFields = computed(() => orderedFields.value.filter(field => field.advanced))

function isWideField(field: SpeechFieldSchema) {
  if (field.type === 'secret') return true
  const key = field.key.toLowerCase()
  if (key.includes('url') || key.includes('endpoint') || key.includes('key') || key.includes('token') || key.includes('path') || key.includes('uri')) return true
  if ((field.description ?? '').length > 80) return true
  return false
}

function stringConfigValue(key: string) {
  const value = configData[key]
  return value == null ? '' : String(value)
}

function enumOptions(field: SpeechFieldSchema): SearchableSelectOption[] {
  return (field.enum ?? []).map(option => ({ value: option, label: option }))
}

function enumPlaceholder(field: SpeechFieldSchema) {
  if (field.key === 'voice') return t('speech.fields.voicePlaceholder')
  if (field.key === 'format') return t('speech.fields.formatPlaceholder')
  return field.title || field.key
}

watch(() => props.config, (cfg) => {
  Object.keys(configData).forEach((key) => delete configData[key])
  Object.assign(configData, { ...(cfg ?? {}) })
  showAdvanced.value = advancedFields.value.some(field => {
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

onBeforeUnmount(revokeAudio)

async function handleSaveConfig() {
  saving.value = true
  try {
    emit('save', buildConfig())
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

    if (mode.value === 'synthesis') {
      const blob = result as Blob
      audioUrl.value = URL.createObjectURL(blob)
      await new Promise<void>((resolve) => setTimeout(resolve, 50))
      audioEl.value?.play()
    } else {
      const payload = result as { text?: string, language?: string }
      transcriptionText.value = payload.text ?? ''
      transcriptionLanguage.value = payload.language ?? ''
    }
  } catch (error: unknown) {
    const msg = error instanceof Error ? error.message : t(mode.value === 'transcription' ? 'transcription.test.failed' : 'speech.test.failed')
    testError.value = msg
    toast.error(msg)
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
