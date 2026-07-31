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
import { computed } from 'vue'
import { SettingsRow, SettingsSection } from '@felinic/ui'
import ModelSelect from './model-select.vue'
import type {
  SettingsSettings,
  AudioSpeechModelResponse,
  AudioSpeechProviderResponse,
  AudioTranscriptionModelResponse,
  AudioTranscriptionProviderResponse,
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
  providers: AudioSpeechProviderResponse[] | AudioTranscriptionProviderResponse[],
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
    metadata: p.metadata,
  }))
}

const props = defineProps<{
  form: SettingsSettings
  ttsModels: AudioSpeechModelResponse[]
  ttsProviders: AudioSpeechProviderResponse[]
  transcriptionModels: AudioTranscriptionModelResponse[]
  transcriptionProviders: AudioTranscriptionProviderResponse[]
  imageCapableModels: ModelsGetResponse[]
  providers: ProvidersGetResponse[]
  videoModels: ModelsGetResponse[]
  videoProviders: VideoProviderResponse[]
}>()

const speechModelOptions = computed(() => toModelOptions(props.ttsModels, 'speech'))
const speechProviderOptions = computed(() => toProviderOptions(props.ttsProviders))
const transcriptionModelOptions = computed(() => toModelOptions(props.transcriptionModels, 'transcription'))
const transcriptionProviderOptions = computed(() => toProviderOptions(props.transcriptionProviders))
</script>
