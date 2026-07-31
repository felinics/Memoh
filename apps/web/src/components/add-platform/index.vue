<template>
  <section class="flex">
    <FormDialogShell
      v-model:open="open"
      :title="$t('platform.addTitle')"
      :description="$t('platform.addDescription')"
      :cancel-text="$t('common.cancel')"
      :submit-text="$t('platform.addTitle')"
      @submit="addPlatform"
    >
      <template #trigger>
        <Button
          variant="default"
          class="ml-auto my-4"
        >
          {{ $t('platform.addTitle') }}
        </Button>
      </template>

      <template #body>
        <div class="flex flex-col gap-3 mt-4">
          <!-- Name -->
          <FormField
            v-slot="{ componentField }"
            name="name"
          >
            <FieldStack
              :label="$t('platform.name')"
              for="platform-create-name"
              reserve-error
            >
              <FormControl>
                <Input
                  id="platform-create-name"
                  type="text"
                  :placeholder="$t('platform.namePlaceholder')"
                  v-bind="componentField"
                  autocomplete="name"
                />
              </FormControl>
            </FieldStack>
          </FormField>

          <!-- Config (key:value tags) -->
          <FormField
            v-slot="{ componentField }"
            name="config"
          >
            <FieldStack
              :label="$t('platform.config')"
              reserve-error
            >
              <FormControl>
                <TagsInput
                  :add-on-blur="true"
                  :model-value="configTags.tagList.value"
                  :convert-value="configTags.convertValue"
                  @update:model-value="(tags) => configTags.handleUpdate(tags.map(String), componentField['onUpdate:modelValue'])"
                >
                  <TagsInputItem
                    v-for="(value, index) in configTags.tagList.value"
                    :key="index"
                    :value="value"
                  >
                    <TagsInputItemText />
                    <TagsInputItemDelete />
                  </TagsInputItem>
                  <TagsInputInput
                    :placeholder="$t('platform.configPlaceholder')"
                    class="w-full py-1"
                  />
                </TagsInput>
              </FormControl>
            </FieldStack>
          </FormField>

          <!-- Active -->
          <FormField
            v-slot="{ componentField }"
            name="active"
          >
            <FieldStack :label="$t('platform.active')">
              <FormControl>
                <Switch
                  :model-value="componentField.modelValue"
                  @update:model-value="componentField['onUpdate:modelValue']"
                />
              </FormControl>
            </FieldStack>
          </FormField>
        </div>
      </template>
    </FormDialogShell>
  </section>
</template>

<script setup lang="ts">
import {
  Button,
  Input,
  FormField,
  FormControl,
  TagsInput,
  TagsInputInput,
  TagsInputItem,
  TagsInputItemDelete,
  TagsInputItemText,
  Switch,
} from '@felinic/ui'
import z from 'zod'
import { toTypedSchema } from '@vee-validate/zod'
import { useForm } from 'vee-validate'
import { useI18n } from 'vue-i18n'
import { FieldStack, FormDialogShell } from '@felinic/ui'
import { useKeyValueTags } from '@/composables/useKeyValueTags'
import { useCreatePlatform } from '@/composables/api/usePlatform'
import { useDialogMutation } from '@/composables/useDialogMutation'

const configTags = useKeyValueTags()
const open = defineModel<boolean>('open', { default: false })
const { t } = useI18n()
const { run } = useDialogMutation()

const validationSchema = toTypedSchema(z.object({
  name: z.string().min(1, t('platform.nameRequired')),
  config: z.looseObject({}),
  active: z.coerce.boolean(),
}))

const form = useForm({ validationSchema })

const { mutateAsync: addFetchPlatform } = useCreatePlatform()

const addPlatform = form.handleSubmit(async (value) => {
  await run(
    () => addFetchPlatform(value),
    {
      fallbackMessage: t('common.saveFailed'),
      onSuccess: () => {
        open.value = false
      },
    },
  )
})
</script>
