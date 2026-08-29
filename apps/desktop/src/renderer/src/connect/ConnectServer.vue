<template>
  <main class="relative flex h-screen w-screen flex-col items-center justify-center overflow-hidden bg-background p-6">
    <DotMatrixBg class="pointer-events-none absolute inset-0" />

    <section
      class="login-scope relative flex w-full max-w-[22rem] -translate-y-8 flex-col items-center gap-6 transition-[opacity,transform] duration-[175ms] ease-out motion-reduce:transition-none"
      :class="exiting ? 'scale-[0.88] opacity-0 motion-reduce:scale-100 motion-reduce:opacity-100' : 'scale-100 opacity-100'"
    >
      <div class="flex flex-col items-center gap-3 text-center">
        <img
          src="/logo.svg"
          class="size-14"
          alt=""
          aria-hidden="true"
        >
        <h1 class="text-display font-semibold text-foreground">
          {{ t('desktopConnection.title') }}
        </h1>
        <p class="max-w-[18rem] text-sm text-muted-foreground">
          {{ t('desktopConnection.subtitle') }}
        </p>
      </div>

      <form
        class="flex w-full flex-col gap-2.5"
        @submit="onSubmit"
      >
        <Input
          v-bind="baseUrlProps"
          id="server-url"
          v-model="baseUrl"
          type="text"
          inputmode="url"
          autocomplete="url"
          :placeholder="t('desktopConnection.placeholder')"
          :disabled="isSubmitting"
          autofocus
        />
        <LoadingButton
          type="submit"
          block
          :loading="isSubmitting"
          :loading-delay="0"
          :disabled="!meta.valid || isSubmitting"
        >
          {{ t('desktopConnection.connect') }}
        </LoadingButton>
      </form>
    </section>

    <div class="absolute bottom-24 left-1/2 w-full max-w-2xl -translate-x-1/2 space-y-1 px-8 text-center text-xs text-muted-foreground">
      <p>
        {{ t('desktopConnection.fullInstallLead') }}
        <a
          href="https://github.com/felinics/Memoh#deploy-to-server"
          target="_blank"
          rel="noreferrer"
          class="underline underline-offset-2 hover:text-foreground"
        >{{ t('desktopConnection.fullInstallAction') }}</a>
      </p>
      <p>{{ t('desktopConnection.fullInstallHint') }}</p>
    </div>
  </main>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { toTypedSchema } from '@vee-validate/zod'
import { useForm } from 'vee-validate'
import { z } from 'zod'
import { Input, toast } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import LoadingButton from '@memohai/web/components/loading-button/index.vue'
import DotMatrixBg from '@memohai/web/pages/login/components/dot-matrix-bg.vue'
import { notifyAuthSessionCleared } from '@memohai/web/lib/auth-session'
import { setupApiClient } from '@memohai/web/api-client'
import { LOGIN_ENTRY_ANIMATION_KEY } from '@memohai/web/pages/login/transition'
import { decidePostConnectNavigation } from './connection-navigation'

const { t } = useI18n()
const router = useRouter()
const route = useRoute()
const exiting = ref(false)
const exitDuration = window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 0 : 175

const { defineField, handleSubmit, isSubmitting, meta, setFieldValue } = useForm({
  validationSchema: toTypedSchema(z.object({
    baseUrl: z.string().trim().min(1),
  })),
  initialValues: { baseUrl: '' },
})
const [baseUrl, baseUrlProps] = defineField('baseUrl', { validateOnModelUpdate: true })

onMounted(async () => {
  const status = await window.api.desktop.getServerStatus()
  setFieldValue('baseUrl', status.baseUrl)
})

const onSubmit = handleSubmit(async ({ baseUrl }) => {
  try {
    const result = await window.api.desktop.connectServer(baseUrl)
    if (!result.ok) {
      const description = result.error === 'http-error' && result.status
        ? t('desktopConnection.errors.http', { status: result.status })
        : t(`desktopConnection.errors.${result.error}`)
      toast.error(t('desktopConnection.failed'), { description })
      return
    }

    exiting.value = true
    await new Promise<void>(resolve => setTimeout(resolve, exitDuration))

    const navigation = decidePostConnectNavigation({
      changed: result.changed,
      hasToken: Boolean(localStorage.getItem('token')),
      returnTo: route.query.returnTo,
    })
    if (navigation.clearAuth) {
      notifyAuthSessionCleared('logout')
      localStorage.removeItem('token')
      localStorage.removeItem('user')
      sessionStorage.clear()
    }
    setupApiClient({
      baseUrl: result.baseUrl,
      onUnauthorized: () => router.replace({ name: 'Login' }),
    })
    if (navigation.animateLogin) {
      sessionStorage.setItem(LOGIN_ENTRY_ANIMATION_KEY, '1')
    }
    await router.replace(navigation.destination)
  } catch {
    exiting.value = false
    toast.error(t('desktopConnection.failed'), {
      description: t('desktopConnection.errors.unreachable'),
    })
  }
})
</script>

<style>
.login-scope {
  --login-control: #ffffff;
  --login-edge-field: inset 0 0 0 1px var(--field-edge-rest);
}

.dark .login-scope {
  --login-control: #242424;
}

.login-scope [data-slot="input"] {
  background-color: var(--login-control);
  box-shadow: var(--login-edge-field);
  transition: background-color 0.2s ease-out, box-shadow 0.06s ease-out;
}

.dark .login-scope [data-slot="input"],
.dark .login-scope [data-slot="input"]:focus {
  box-shadow: none;
}

.login-scope [data-slot="input"]:focus {
  box-shadow: var(--login-edge-field);
}

.login-scope [data-slot="input"]:disabled {
  background-color: color-mix(in oklab, var(--background) 55%, var(--login-control));
  opacity: 1;
  cursor: not-allowed;
}
</style>
