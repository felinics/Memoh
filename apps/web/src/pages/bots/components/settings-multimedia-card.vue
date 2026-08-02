<!-- eslint-disable vue/no-mutating-props -->
<template>
  <SettingsSection :title="$t('bots.settings.blocks.multimedia')">
    <SettingsRow :label="$t('bots.settings.ttsModel')">
      <div class="w-56">
        <ModelSelect
          v-model="form.tts_model_id"
          :models="speechModelOptions"
          :providers="speechProviderOptions"
          model-type="speech"
          :placeholder="$t('bots.settings.ttsModelPlaceholder')"
          :none-label="$t('common.none')"
        />
      </div>
    </SettingsRow>

    <SettingsRow :label="$t('bots.settings.transcriptionModel')">
      <div class="w-56">
        <ModelSelect
          v-model="form.transcription_model_id"
          :models="transcriptionModelOptions"
          :providers="transcriptionProviderOptions"
          model-type="transcription"
          :placeholder="$t('bots.settings.transcriptionModelPlaceholder')"
          :none-label="$t('common.none')"
        />
      </div>
    </SettingsRow>

    <SettingsRow
      :label="$t('bots.settings.imageModel')"
      :description="$t('bots.settings.imageModelDescription')"
    >
      <div class="w-56">
        <ModelSelect
          v-model="form.image_model_id"
          :models="imageCapableModels"
          :providers="providers"
          model-type="chat"
          :placeholder="$t('bots.settings.imageModelPlaceholder')"
        />
      </div>
    </SettingsRow>

    <SettingsRow
      :label="$t('bots.settings.auxiliaryVisionMode')"
      :description="$t('bots.settings.auxiliaryVisionDescription')"
    >
      <div class="w-56">
        <Select v-model="form.auxiliary_vision_mode">
          <SelectTrigger class="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="inherit">
              {{ $t('bots.settings.auxiliaryVisionModeInherit') }}
            </SelectItem>
            <SelectItem value="enabled">
              {{ $t('bots.settings.auxiliaryVisionModeEnabled') }}
            </SelectItem>
            <SelectItem value="disabled">
              {{ $t('bots.settings.auxiliaryVisionModeDisabled') }}
            </SelectItem>
          </SelectContent>
        </Select>
      </div>
    </SettingsRow>

    <template v-if="form.auxiliary_vision_mode === 'enabled'">
      <SettingsRow
        :label="$t('bots.settings.auxiliaryVisionModel')"
        :description="$t('bots.settings.auxiliaryVisionModelDescription')"
      >
        <div class="w-56">
          <ModelSelect
            v-model="form.auxiliary_vision_model_id"
            :models="visionCapableModels"
            :providers="providers"
            model-type="chat"
            :placeholder="$t('bots.settings.auxiliaryVisionServerDefault')"
            :none-label="$t('bots.settings.auxiliaryVisionServerDefault')"
          />
        </div>
      </SettingsRow>

      <ExpandableSettingsRow
        v-model:open="auxiliaryVisionAdvancedOpen"
        :label="$t('bots.settings.auxiliaryVisionAdvanced')"
        :description="$t('bots.settings.auxiliaryVisionAdvancedDescription')"
      >
        <template #expanded>
          <FormStack>
            <FieldStack
              :label="$t('bots.settings.auxiliaryVisionPrompt')"
              :help="$t('bots.settings.auxiliaryVisionPromptHelp')"
              for="auxiliary-vision-prompt"
            >
              <Textarea
                id="auxiliary-vision-prompt"
                v-model="form.auxiliary_vision_prompt"
                :placeholder="$t('bots.settings.auxiliaryVisionPromptPlaceholder')"
                :rows="4"
              />
            </FieldStack>

            <FieldStack
              :label="$t('bots.settings.auxiliaryVisionMaxRetries')"
              :help="$t('bots.settings.auxiliaryVisionMaxRetriesHelp')"
              for="auxiliary-vision-max-retries"
            >
              <Input
                id="auxiliary-vision-max-retries"
                :model-value="(form.auxiliary_vision_max_retries ?? -1) < 0 ? '' : form.auxiliary_vision_max_retries"
                type="number"
                :min="0"
                :max="10"
                :placeholder="$t('bots.settings.auxiliaryVisionServerDefault')"
                @update:model-value="(raw) => form.auxiliary_vision_max_retries = optionalNumber(raw, -1)"
              />
            </FieldStack>

            <FieldStack
              :label="$t('bots.settings.auxiliaryVisionTimeout')"
              :help="$t('bots.settings.auxiliaryVisionTimeoutHelp')"
              for="auxiliary-vision-timeout"
            >
              <Input
                id="auxiliary-vision-timeout"
                :model-value="(form.auxiliary_vision_timeout_seconds ?? 0) <= 0 ? '' : form.auxiliary_vision_timeout_seconds"
                type="number"
                :min="1"
                :max="86400"
                :placeholder="$t('bots.settings.auxiliaryVisionServerDefault')"
                @update:model-value="(raw) => form.auxiliary_vision_timeout_seconds = optionalNumber(raw, 0)"
              />
            </FieldStack>
          </FormStack>
        </template>
      </ExpandableSettingsRow>
    </template>

    <SettingsRow
      :label="$t('bots.settings.videoModel')"
      :description="$t('bots.settings.videoModelDescription')"
    >
      <div class="w-56">
        <ModelSelect
          v-model="form.video_model_id"
          :models="videoModels"
          :providers="videoProviders"
          model-type="video"
          :placeholder="$t('bots.settings.videoModelPlaceholder')"
        />
      </div>
    </SettingsRow>
  </SettingsSection>
</template>

<script setup lang="ts">
import {
  ExpandableSettingsRow,
  FieldStack,
  FormStack,
  Input,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Textarea,
} from '@felinic/ui'
import { computed, ref } from 'vue'
import ModelSelect from './model-select.vue'
import SettingsSection from '@/components/settings/section.vue'
import SettingsRow from '@/components/settings/row.vue'
import type {
  SettingsSettings,
  AudioSpeechModelResponse,
  AudioSpeechProviderResponse,
  AudioTranscriptionModelResponse,
  ModelsGetResponse,
  ProvidersGetResponse,
  VideoProviderResponse,
} from '@memohai/sdk'

function toModelOptions(
  models: AudioSpeechModelResponse[] | AudioTranscriptionModelResponse[],
  type: 'speech' | 'transcription',
): ModelsGetResponse[] {
  return models.map((m) => ({
    id: m.id,
    model_id: m.model_id,
    name: m.name,
    provider_id: m.provider_id,
    type,
  }))
}

function toProviderOptions(
  providers: AudioSpeechProviderResponse[],
): ProvidersGetResponse[] {
  return providers.map((p) => ({
    id: p.id,
    name: p.name,
    icon: p.icon,
    enable: p.enable,
    client_type: p.client_type,
    config: p.config,
    created_at: p.created_at,
    updated_at: p.updated_at,
  }))
}

const props = defineProps<{
  form: SettingsSettings
  ttsModels: AudioSpeechModelResponse[]
  ttsProviders: AudioSpeechProviderResponse[]
  transcriptionModels: AudioTranscriptionModelResponse[]
  transcriptionProviders: AudioSpeechProviderResponse[]
  imageCapableModels: ModelsGetResponse[]
  visionCapableModels: ModelsGetResponse[]
  providers: ProvidersGetResponse[]
  videoModels: ModelsGetResponse[]
  videoProviders: VideoProviderResponse[]
}>()

const speechModelOptions = computed(() => toModelOptions(props.ttsModels, 'speech'))
const speechProviderOptions = computed(() => toProviderOptions(props.ttsProviders))
const transcriptionModelOptions = computed(() => toModelOptions(props.transcriptionModels, 'transcription'))
const transcriptionProviderOptions = computed(() => toProviderOptions(props.transcriptionProviders))
const auxiliaryVisionAdvancedOpen = ref(false)

function optionalNumber(raw: string | number, fallback: number): number {
  const value = Number(raw)
  return raw === '' || !Number.isFinite(value) ? fallback : value
}
</script>
