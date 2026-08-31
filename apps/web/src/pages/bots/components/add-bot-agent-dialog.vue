<template>
  <FormDialogShell
    v-model:open="open"
    :title="t('bots.agent.add')"
    :cancel-text="t('common.cancel')"
    :submit-text="t('bots.agent.add')"
    :submit-disabled="form.meta.value.valid === false || isLoading || providerOptions.length === 0"
    :loading="isLoading"
    @submit="createAgent"
  >
    <template #body>
      <FormStack class="mt-4">
        <FormField
          v-slot="{ value, handleChange }"
          name="provider"
        >
          <FieldStack :label="t('bots.agent.provider')">
            <FormControl>
              <SearchableSelectPopover
                :model-value="String(value ?? '')"
                :options="providerOptions"
                :placeholder="t('bots.agent.providerPlaceholder')"
                :search-placeholder="t('bots.agent.providerSearchPlaceholder')"
                :empty-text="t('bots.agent.providerEmpty')"
                @update:model-value="(provider) => selectProvider(provider, handleChange)"
              >
                <template #trigger="{ open: providerOpen, displayLabel, selectedOption, placeholder }">
                  <button
                    data-slot="select-trigger"
                    data-size="default"
                    :data-placeholder="displayLabel ? undefined : ''"
                    type="button"
                    :aria-expanded="providerOpen"
                    :aria-label="t('bots.agent.provider')"
                    :class="[selectTriggerClass, 'w-full']"
                  >
                    <span class="flex min-w-0 items-center gap-2">
                      <component
                        :is="acpAgentIcon(selectedOption?.value ?? '', true)"
                        v-if="selectedOption"
                        class="size-4 shrink-0"
                      />
                      <span class="line-clamp-1">{{ displayLabel || placeholder }}</span>
                    </span>
                    <ChevronsUpDown class="opacity-50" />
                  </button>
                </template>
                <template #option-icon="{ option }">
                  <component
                    :is="acpAgentIcon(option.value, true)"
                    class="size-4 shrink-0"
                  />
                </template>
              </SearchableSelectPopover>
            </FormControl>
          </FieldStack>
        </FormField>

        <FormField
          v-slot="{ componentField }"
          name="name"
        >
          <FieldStack
            :label="t('common.name')"
            for="bot-agent-create-name"
          >
            <FormControl>
              <Input
                id="bot-agent-create-name"
                v-bind="componentField"
                type="text"
                :placeholder="t('bots.agent.namePlaceholder')"
                :aria-label="t('common.name')"
              />
            </FormControl>
          </FieldStack>
        </FormField>
      </FormStack>
    </template>
  </FormDialogShell>
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMutation, useQueryCache } from '@pinia/colada'
import { toTypedSchema } from '@vee-validate/zod'
import { useForm } from 'vee-validate'
import { ChevronsUpDown } from 'lucide-vue-next'
import z from 'zod'
import {
  FieldStack,
  FormControl,
  FormDialogShell,
  FormField,
  FormStack,
  Input,
  selectTriggerClass,
  toast,
} from '@felinic/ui'
import {
  postBotsByBotIdAgents,
  putBotsById,
  type AcpprofilePublicProfile,
  type BotagentsBotAgent,
} from '@memohai/sdk'
import SearchableSelectPopover from '@/components/searchable-select-popover/index.vue'
import { useDialogMutation } from '@/composables/useDialogMutation'
import {
  acpAgentIcon,
  normalizeACPAgentID,
  withEnabledACPAgentMetadataIfConfigured,
} from '@/utils/acp'
import {
  BOT_AGENT_RUNTIME_ACP,
  botAgentRuntimeOptions,
  suggestBotAgentName,
  directBotAgentMetadata,
} from '@/utils/bot-agent'

const open = defineModel<boolean>('open')
const props = defineProps<{
  botId: string
  profiles: AcpprofilePublicProfile[]
  agents: BotagentsBotAgent[]
  botMetadata?: Record<string, unknown>
}>()
const emit = defineEmits<{
  created: [agent: BotagentsBotAgent]
}>()

const { t } = useI18n()
const queryCache = useQueryCache()
const { run } = useDialogMutation()

const providerOptions = computed(() => botAgentRuntimeOptions(props.profiles))

const schema = toTypedSchema(z.object({
  provider: z.string().trim().min(1, t('bots.agent.providerRequired')),
  name: z.string().trim().min(1, t('bots.agent.nameRequired')),
}))

const form = useForm({
  validationSchema: schema,
  initialValues: { provider: '', name: '' },
})

function providerDefaultName(provider: string): string {
  const option = providerOptions.value.find(item => item.value === provider)
  return suggestBotAgentName(provider, props.agents, option?.label ?? '')
}

function resetForm() {
  const provider = providerOptions.value[0]?.value ?? ''
  form.resetForm({
    values: {
      provider,
      name: provider ? providerDefaultName(provider) : '',
    },
  })
}

function selectProvider(provider: string, handleChange: (value: string) => void) {
  const normalized = normalizeACPAgentID(provider)
  handleChange(normalized)
  form.setFieldValue('name', normalized ? providerDefaultName(normalized) : '')
}

const { mutateAsync: createMutation, isLoading } = useMutation({
  mutation: async (value: { provider: string; name: string }) => {
    const provider = normalizeACPAgentID(value.provider)
    const option = providerOptions.value.find(item => item.value === provider)
    if (!option) throw new Error(t('bots.agent.providerRequired'))

    let agentMetadata: Record<string, unknown> = { provider }
    if (option.runtime === BOT_AGENT_RUNTIME_ACP) {
      const profile = props.profiles.find(item => normalizeACPAgentID(item.id) === provider)
      if (!profile) throw new Error(t('bots.agent.providerRequired'))
      const metadata = withEnabledACPAgentMetadataIfConfigured(props.botMetadata, profile)
      if (metadata) {
        await putBotsById({
          path: { id: props.botId },
          body: { metadata },
          throwOnError: true,
        })
      }
    } else {
      agentMetadata = directBotAgentMetadata(option.runtime) ?? agentMetadata
    }

    const { data } = await postBotsByBotIdAgents({
      path: { bot_id: props.botId },
      body: {
        name: value.name.trim(),
        runtime: option.runtime,
        metadata: agentMetadata,
      },
      throwOnError: true,
    })
    return data
  },
  onSettled: () => {
    void queryCache.invalidateQueries({ key: ['bot-agents', props.botId] })
    void queryCache.invalidateQueries({ key: ['bot', props.botId] })
  },
})

const createAgent = form.handleSubmit(async (value) => {
  await run(
    () => createMutation(value),
    {
      fallbackMessage: t('common.saveFailed'),
      onSuccess: (agent) => {
        open.value = false
        resetForm()
        toast.success(t('bots.agent.added'))
        emit('created', agent)
      },
    },
  )
})

watch(open, (value) => {
  if (value) resetForm()
})
</script>
