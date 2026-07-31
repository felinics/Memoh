<template>
  <Dialog
    :open="open"
    @update:open="$emit('update:open', $event)"
  >
    <DialogContent class="sm:max-w-lg">
      <DialogHeader>
        <DialogTitle>{{ $t('supermarket.pluginInstallTitle') }}</DialogTitle>
      </DialogHeader>
      <div class="space-y-4 py-2">
        <FieldStack :label="$t('supermarket.selectBot')">
          <BotSelect
            v-model="selectedBotId"
            trigger-class="w-full"
          />
        </FieldStack>

        <div
          v-if="plugin"
          class="rounded-md border border-border p-3 space-y-1"
        >
          <div class="flex min-w-0 flex-wrap items-center gap-2">
            <p
              class="min-w-0 truncate text-xs font-medium"
              :title="plugin.name"
            >
              {{ plugin.name }}
            </p>
            <Badge
              v-if="plugin.mcps?.length"
              variant="outline"
              size="sm"
            >
              {{ plugin.mcps.length }} MCPs
            </Badge>
            <Badge
              v-if="pluginSkills.length"
              variant="outline"
              size="sm"
            >
              {{ pluginSkills.length }} Skills
            </Badge>
          </div>
          <p class="text-caption text-muted-foreground line-clamp-3">
            {{ plugin.description }}
          </p>
          <div
            v-if="pluginSkills.length"
            class="mt-3 grid gap-1.5"
          >
            <div
              v-for="skill in pluginSkills"
              :key="skillKey(skill)"
              class="flex min-w-0 items-start gap-2 rounded border border-border-soft bg-muted/20 px-2 py-1.5"
            >
              <Boxes class="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
              <div class="min-w-0 flex-1">
                <p
                  class="truncate text-caption font-medium"
                  :title="skillName(skill)"
                >
                  {{ skillName(skill) }}
                </p>
                <p
                  class="truncate text-[10px] text-muted-foreground"
                  :title="skillDescription(skill)"
                >
                  {{ skillDescription(skill) }}
                </p>
              </div>
            </div>
          </div>
        </div>

        <div
          v-if="variables.length"
          class="space-y-3"
        >
          <FieldStack
            v-for="item in variables"
            :key="item.key"
          >
            <template #label>
              <Label>
                {{ item.key }}
                <span
                  v-if="item.required"
                  class="text-destructive"
                >*</span>
              </Label>
            </template>
            <Input
              v-model="variableValues[item.key || '']"
              :type="item.secret ? 'password' : 'text'"
              class="h-8 text-xs"
              :placeholder="item.description || item.key"
            />
          </FieldStack>
        </div>
      </div>
      <DialogFooter>
        <DialogClose as-child>
          <Button
            variant="outline"
            :disabled="installing"
          >
            {{ $t('common.cancel') }}
          </Button>
        </DialogClose>
        <Button
          :disabled="!canInstall"
          :loading="installing"
          @click="handleInstall"
        >
          {{ installing ? $t('supermarket.installing') : $t('supermarket.install') }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { Boxes } from 'lucide-vue-next'
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter, DialogClose,
  Button, Badge, Input, Label, toast,
} from '@felinic/ui'
import {
  getBotsByBotIdPluginsByIdOauthStatus,
  postBotsByBotIdPluginsByIdOauthAuthorize,
  postBotsByBotIdSupermarketInstallPlugin,
  type PluginsConfigVar,
  type PluginsInstallation,
  type PluginsManifest,
  type PluginsSkillEntry,
  type PluginsSkillResource,
} from '@memohai/sdk'
import { client } from '@memohai/sdk/client'
import { FieldStack } from '@felinic/ui'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { emitBotPluginsUpdated } from '@/utils/bot-plugin-events'
import BotSelect from '@/components/bot-select/index.vue'

const props = defineProps<{
  open: boolean
  plugin: PluginsManifest | null
}>()

const emit = defineEmits<{
  'update:open': [open: boolean]
  'installed': []
}>()

const { t } = useI18n()
const router = useRouter()

const selectedBotId = ref('')
const installing = ref(false)
const variableValues = reactive<Record<string, string>>({})

const variables = computed<PluginsConfigVar[]>(() => props.plugin?.variables ?? [])
type PluginSkill = PluginsSkillEntry | PluginsSkillResource

const pluginSkills = computed<PluginSkill[]>(() => [
  ...(props.plugin?.bundled_skills ?? []),
  ...(props.plugin?.skills ?? []),
])

const requiresManagedOAuth = computed(() => {
  return (props.plugin?.auth_requirements ?? []).some(item => item.type === 'managed_oauth')
})
const canInstall = computed(() => {
  if (!selectedBotId.value || !props.plugin?.id) return false
  return variables.value.every(item => !item.required || !!variableValues[item.key || '']?.trim())
})

function skillKey(skill: PluginSkill): string {
  if ('id' in skill && skill.id) return skill.id
  if ('key' in skill && skill.key) return skill.key
  return skillName(skill)
}

function skillName(skill: PluginSkill): string {
  if ('name' in skill && skill.name) return skill.name
  if ('id' in skill && skill.id) return skill.id
  if ('key' in skill && skill.key) return skill.key
  return t('supermarket.unnamedSkill')
}

function skillDescription(skill: PluginSkill): string {
  if ('description' in skill && skill.description) return skill.description
  if ('path' in skill && skill.path) return skill.path
  return t('supermarket.noDescription')
}

watch(() => props.open, (open) => {
  if (!open) {
    selectedBotId.value = ''
    installing.value = false
    for (const key of Object.keys(variableValues)) delete variableValues[key]
    return
  }
  for (const item of props.plugin?.variables ?? []) {
    if (item.key) variableValues[item.key] = item.defaultValue || ''
  }
})

async function handleInstall() {
  if (!selectedBotId.value || !props.plugin?.id) return
  const botId = selectedBotId.value
  const oauthPopup = requiresManagedOAuth.value && !canOpenOAuthExternally()
    ? window.open('', 'mcp-oauth', 'width=600,height=700')
    : null
  installing.value = true
  try {
    const { data } = await postBotsByBotIdSupermarketInstallPlugin({
      path: { bot_id: botId },
      body: {
        plugin_id: props.plugin.id,
        variables: Object.fromEntries(
          Object.entries(variableValues).filter(([, value]) => value.trim() !== ''),
        ),
      },
      throwOnError: true,
    })
    toast.success(t('supermarket.installSuccess'))
    emitBotPluginsUpdated(botId)
    emit('update:open', false)
    emit('installed')
    void router.push({
      name: 'bot-detail',
      params: { botName: botId },
      query: { tab: 'plugins' },
    }).catch(() => {})
    if (data.status === 'needs_auth' && data.id) {
      await startOAuthAfterInstall(botId, data, oauthPopup)
    } else {
      oauthPopup?.close()
    }
  } catch (error) {
    oauthPopup?.close()
    toast.error(resolveApiErrorMessage(error, t('supermarket.installFailed')))
  } finally {
    installing.value = false
  }
}

function mcpOAuthCallbackUrl() {
  const rawBase = String(client.getConfig().baseUrl || '/api')
  const base = new URL(rawBase, window.location.origin)
  const basePath = base.pathname.replace(/\/+$/, '')
  base.pathname = `${basePath}/oauth/mcp/callback`
  base.search = ''
  base.hash = ''
  return base.toString()
}

async function startOAuthAfterInstall(botId: string, installation: PluginsInstallation, popup: Window | null) {
  try {
    const { data } = await postBotsByBotIdPluginsByIdOauthAuthorize({
      path: { bot_id: botId, id: installation.id! },
      body: { callback_url: mcpOAuthCallbackUrl() },
      throwOnError: true,
    })
    if (!data.authorization_url) throw new Error(t('mcp.oauth.flowInitFailed'))

    if (popup && !popup.closed) {
      popup.location.href = data.authorization_url
    } else {
      popup = await openOAuthURL(data.authorization_url)
    }

    await waitForMCPOAuth(botId, installation.id!, popup)
    const synced = await syncOAuthStatus(botId, installation.id!)
    if (synced.status !== 'ready' && !synced.enabled) throw new Error(t('mcp.oauth.authFailed'))
    emitBotPluginsUpdated(botId)
    toast.success(t('mcp.oauth.authSuccess'))
    emit('installed')
  } catch (error) {
    const synced = await syncOAuthStatus(botId, installation.id!).catch(() => null)
    if (synced) emitBotPluginsUpdated(botId)
    if (synced?.status === 'ready' || synced?.enabled) {
      toast.success(t('mcp.oauth.authSuccess'))
      emit('installed')
      return
    }
    popup?.close()
    toast.error(resolveApiErrorMessage(error, t('mcp.oauth.flowInitFailed')))
  }
}

function canOpenOAuthExternally(): boolean {
  return Boolean(window.api?.desktop?.openExternalUrl)
}

async function openOAuthURL(url: string): Promise<Window | null> {
  const desktopOpenExternal = window.api?.desktop?.openExternalUrl
  if (desktopOpenExternal) {
    await desktopOpenExternal(url)
    return null
  }
  return window.open(url, 'mcp-oauth', 'width=600,height=700')
}

function waitForMCPOAuth(botId: string, installationId: string, popup: Window | null): Promise<void> {
  return new Promise((resolve, reject) => {
    let completed = false
    const startedAt = Date.now()
    let pollTimer: ReturnType<typeof setInterval> | undefined

    const finish = (status: 'success' | 'error', error?: string) => {
      if (completed) return
      completed = true
      if (pollTimer) clearInterval(pollTimer)
      window.removeEventListener('message', onMessage)
      if (status === 'success') {
        resolve()
      } else {
        reject(new Error(error || t('mcp.oauth.authFailed')))
      }
    }

    const onMessage = (event: MessageEvent) => {
      if (event.data?.type !== 'mcp-oauth-callback') return
      finish(event.data.status === 'success' ? 'success' : 'error', event.data.error)
    }

    window.addEventListener('message', onMessage)
    pollTimer = setInterval(() => {
      getBotsByBotIdPluginsByIdOauthStatus({
        path: { bot_id: botId, id: installationId },
        throwOnError: true,
      }).then(({ data }) => {
        if (completed) return
        if (data.status === 'ready' || data.enabled) {
          finish('success')
          return
        }
        if ((popup && popup.closed) || Date.now() - startedAt > 120_000) {
          finish('error')
        }
      }).catch(() => {
        if (!completed && ((popup && popup.closed) || Date.now() - startedAt > 120_000)) {
          finish('error')
        }
      })
    }, 2000)
  })
}

async function syncOAuthStatus(botId: string, installationId: string): Promise<PluginsInstallation> {
  const { data } = await getBotsByBotIdPluginsByIdOauthStatus({
    path: { bot_id: botId, id: installationId },
    throwOnError: true,
  })
  return data
}
</script>
