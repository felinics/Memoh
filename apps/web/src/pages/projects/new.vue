<template>
  <section class="relative mx-auto w-full max-w-2xl px-4 pb-10 pt-2 md:pb-12 md:pt-4 lg:px-6">
    <h1 class="mb-6 text-lg font-semibold">
      {{ t('projects.newProject') }}
    </h1>

    <form
      class="space-y-4"
      @submit.prevent="handleSubmit"
    >
      <div class="space-y-1.5">
        <Label for="project-name">
          {{ t('projects.projectName') }}
          <span class="text-destructive">*</span>
        </Label>
        <Input
          id="project-name"
          ref="nameInputRef"
          v-model="name"
          :placeholder="t('projects.projectNamePlaceholder')"
          autocomplete="off"
        />
      </div>

      <div class="space-y-1.5">
        <Label for="project-description">
          {{ t('projects.projectDescription') }}
          <span class="text-muted-foreground">{{ t('common.optional') }}</span>
        </Label>
        <Textarea
          id="project-description"
          v-model="description"
          :placeholder="t('projects.projectDescriptionPlaceholder')"
          rows="3"
        />
      </div>

      <div class="flex justify-end gap-2 pt-2">
        <Button
          variant="ghost"
          type="button"
          @click="router.back()"
        >
          {{ t('common.cancel') }}
        </Button>
        <Button
          type="submit"
          :disabled="!canSubmit"
        >
          <Spinner v-if="submitting" />
          {{ t('common.create') }}
        </Button>
      </div>
    </form>
  </section>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Button, Input, Label, Spinner, Textarea, toast } from '@felinic/ui'
import { useQueryCache } from '@pinia/colada'
import { postProjects } from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'

const { t } = useI18n()
const router = useRouter()
const queryCache = useQueryCache()

const name = ref('')
const description = ref('')
const submitting = ref(false)

const canSubmit = computed(() => name.value.trim().length > 0 && !submitting.value)

// The page's job is to type a name, so the caret starts there.
onMounted(() => {
  void nextTick(() => {
    document.getElementById('project-name')?.focus()
  })
})

async function handleSubmit() {
  if (!canSubmit.value) return
  submitting.value = true
  try {
    const { data } = await postProjects({
      body: { name: name.value.trim(), description: description.value.trim() },
      throwOnError: true,
    })
    queryCache.invalidateQueries({ key: ['projects'] })
    toast.success(t('projects.createSuccess'))
    if (data?.id) {
      await router.replace({ name: 'project-detail', params: { projectId: data.id } })
    } else {
      await router.replace({ name: 'projects' })
    }
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('projects.saveFailed')))
  } finally {
    submitting.value = false
  }
}
</script>
