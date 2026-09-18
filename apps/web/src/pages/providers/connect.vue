<template>
  <div class="flex items-center justify-center min-h-screen bg-background text-foreground p-4">
    <Card class="w-full max-w-md">
      <template v-if="errorReason">
        <CardHeader class="items-center text-center">
          <CircleX class="size-8 text-destructive" />
          <CardTitle>{{ t('providerConnect.invalidTitle') }}</CardTitle>
          <CardDescription>{{ t(`providerConnect.error.${errorReason}`) }}</CardDescription>
        </CardHeader>
        <CardFooter class="justify-center">
          <Button
            variant="outline"
            @click="goBack"
          >
            {{ t('providerConnect.backToProviders') }}
          </Button>
        </CardFooter>
      </template>

      <template v-else-if="payload">
        <CardHeader>
          <CardTitle>{{ t('providerConnect.title') }}</CardTitle>
          <CardDescription>{{ t('providerConnect.description') }}</CardDescription>
        </CardHeader>
        <CardContent>
          <dl class="rounded-lg border divide-y text-sm">
            <div class="flex justify-between gap-4 px-4 py-2.5">
              <dt class="text-muted-foreground shrink-0">
                {{ t('providerConnect.name') }}
              </dt>
              <dd class="font-medium truncate">
                {{ payload.name }}
              </dd>
            </div>
            <div class="flex justify-between gap-4 px-4 py-2.5">
              <dt class="text-muted-foreground shrink-0">
                {{ t('providerConnect.baseUrl') }}
              </dt>
              <dd class="font-mono text-xs truncate self-center">
                {{ payload.baseUrl }}
              </dd>
            </div>
            <div class="flex justify-between gap-4 px-4 py-2.5">
              <dt class="text-muted-foreground shrink-0">
                {{ t('providerConnect.clientType') }}
              </dt>
              <dd class="font-mono text-xs self-center">
                {{ payload.clientType }}
              </dd>
            </div>
            <div class="flex justify-between gap-4 px-4 py-2.5">
              <dt class="text-muted-foreground shrink-0">
                {{ t('providerConnect.apiKey') }}
              </dt>
              <dd class="font-mono text-xs self-center">
                {{ maskApiKey(payload.apiKey) }}
              </dd>
            </div>
          </dl>
        </CardContent>
        <CardFooter class="justify-end gap-3">
          <Button
            variant="outline"
            :disabled="creating"
            @click="goBack"
          >
            {{ t('providerConnect.decline') }}
          </Button>
          <Button
            :loading="creating"
            @click="confirm"
          >
            {{ t('providerConnect.confirm') }}
          </Button>
        </CardFooter>
      </template>

      <CardContent
        v-else
        class="flex justify-center py-8"
      >
        <Spinner class="size-8" />
      </CardContent>
    </Card>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useQueryCache } from '@pinia/colada'
import {
  Button,
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
  Spinner,
  toast,
} from '@felinic/ui'
import { CircleX } from 'lucide-vue-next'
import {
  getProviders,
  getProviderTemplates,
  postProviders,
  postProvidersByIdImportModels,
  postProvidersFromTemplate,
} from '@memohai/sdk'
import {
  maskApiKey,
  parseProviderConnectPayload,
  PayloadError,
  type ProviderConnectError,
  type ProviderConnectPayload,
} from '@/utils/provider-connect'
import { resolveApiErrorMessage } from '@/utils/api-error'

const router = useRouter()
const { t } = useI18n()
const queryCache = useQueryCache()

const payload = ref<ProviderConnectPayload>()
const errorReason = ref<ProviderConnectError>()
const creating = ref(false)

function readPayloadFromHash() {
  const encoded = new URLSearchParams(window.location.hash.slice(1)).get('payload') ?? ''
  // The link is a bearer credential: scrub the fragment from the current
  // history entry right after reading it so the key doesn't linger in browser
  // history (and history sync) after the hop. Unconditional — an unparseable
  // payload still carries whatever the sender put in the URL.
  window.history.replaceState(window.history.state, '', window.location.pathname)
  // A create already in flight owns the screen: both buttons are disabled and
  // confirm() captured its payload, so swapping the card under it would only
  // show something the user cannot act on. The fragment is scrubbed either way.
  if (creating.value) return
  payload.value = undefined
  errorReason.value = undefined
  try {
    payload.value = parseProviderConnectPayload(encoded)
  } catch (err) {
    errorReason.value = err instanceof PayloadError ? err.reason : 'decode'
  }
}

onMounted(() => {
  readPayloadFromHash()
  // A second import link differs from the scrubbed URL only by its fragment,
  // so the browser treats it as a same-document navigation: no reload, no
  // remount, and vue-router never sees it either. Without this listener the
  // page would keep showing — and confirming — the FIRST link's provider while
  // the second link's API key sat unscrubbed in the address bar and history.
  window.addEventListener('hashchange', readPayloadFromHash)
})

onBeforeUnmount(() => {
  window.removeEventListener('hashchange', readPayloadFromHash)
})

function goBack() {
  router.replace({ name: 'providers' })
}

async function confirm() {
  const p = payload.value
  if (!p || creating.value) return
  creating.value = true
  try {
    const config = { base_url: p.baseUrl, api_key: p.apiKey }

    // A matching registry template supplies the icon and default config;
    // anything else lands as a plain custom provider with the given API format.
    // Payload's `template` is the registry key (e.g. `newapi`); the create API
    // wants the template's UUID. Templates fix the client type to their driver,
    // so a template whose driver disagrees with the payload's client_type is no
    // match at all — honor client_type and fall back to custom creation.
    let templateId: string | undefined
    if (p.template) {
      const { data: templates } = await getProviderTemplates({
        query: { domain: 'llm' },
        throwOnError: true,
      })
      const wanted = p.template.toLowerCase()
      const hit = (templates ?? []).find(tpl =>
        (tpl.key?.toLowerCase() === wanted || tpl.id === p.template)
        && (tpl.driver ?? 'openai-completions') === p.clientType)
      templateId = hit?.id
    }

    const created = templateId
      ? (await postProvidersFromTemplate({
          body: { template_id: templateId, domain: 'llm', name: p.name, config },
          throwOnError: true,
        })).data
      : (await postProviders({
          body: { name: p.name, client_type: p.clientType, config },
          throwOnError: true,
        })).data

    if (!created?.id) throw new Error('provider creation returned no id')

    // Best-effort: the model list can always be refreshed from the detail page.
    try {
      await postProvidersByIdImportModels({ path: { id: created.id }, throwOnError: true })
    } catch {
      toast.error(t('models.importFailed'))
    }

    // Seed the providers cache with the post-create list BEFORE navigating.
    // The settings page resolves `?provider=<id>` against that cached list on
    // its very first frame, and `['providers']` is a persisted query: a list
    // hydrated from a previous session does not contain the provider we just
    // made, so the page would read the id as deleted, strip the query and drop
    // the user on the list instead of the provider they just imported.
    // Best-effort — on failure the page still revalidates on mount.
    try {
      const { data: providers } = await getProviders({ throwOnError: true })
      queryCache.setQueryData(['providers'], providers)
    } catch {
      // ignored: invalidation below still refreshes the list on mount
    }

    // After setQueryData (which marks the entry fresh), so the page also
    // revalidates against the server once it mounts.
    queryCache.invalidateQueries({ key: ['providers'] })
    queryCache.invalidateQueries({ key: ['models'] })

    router.replace({
      name: 'providers',
      query: {
        provider: created.provider_template_id
          ? `template:${created.provider_template_id}`
          : created.id,
      },
    })
  } catch (err) {
    toast.error(resolveApiErrorMessage(err, t('providerConnect.createFailed')))
    creating.value = false
  }
}
</script>
