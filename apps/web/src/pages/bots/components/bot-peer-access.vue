<template>
  <SettingsSection>
    <SettingsRow
      :label="$t('bots.access.peerAccess.title')"
      :description="$t('bots.access.peerAccess.subtitle')"
    >
      <Button
        v-if="!formVisible"
        size="sm"
        variant="outline"
        class="shrink-0"
        @click="openAddForm"
      >
        <Plus class="size-4" />
        {{ $t('bots.access.peerAccess.addBot') }}
      </Button>
    </SettingsRow>

    <!-- Inline add-bot form: same one-off disclosure frame as the member list
         above, so the two subject families read as one Access surface. -->
    <div
      v-if="formVisible"
      class="mx-4 border-b border-border py-4"
    >
      <FormStack>
        <FieldStack>
          <template #label>
            <Label class="text-xs font-medium text-muted-foreground">
              {{ $t('bots.access.peerAccess.subjectQuestion') }}
            </Label>
          </template>
          <SegmentedControl
            :model-value="formSubjectType"
            :items="subjectTypeItems"
            :aria-label="$t('bots.access.peerAccess.subjectQuestion')"
            class="w-full sm:w-fit"
            @update:model-value="(value) => formSubjectType = value as 'bot' | 'any_bot'"
          />
        </FieldStack>

        <FieldStack v-if="formSubjectType === 'bot'">
          <template #label>
            <Label class="text-xs font-medium text-muted-foreground">
              {{ $t('bots.access.peerAccess.botQuestion') }}
            </Label>
          </template>
          <SearchableSelectPopover
            v-model="formSubjectBotId"
            :options="candidateOptions"
            :placeholder="$t('bots.access.peerAccess.selectBot')"
            :search-placeholder="$t('bots.access.peerAccess.searchBot')"
            :empty-text="$t('bots.access.peerAccess.noBotCandidates')"
            :show-group-headers="false"
          />
        </FieldStack>

        <FieldStack>
          <template #label>
            <Label class="text-xs font-medium text-muted-foreground">
              {{ $t('bots.access.peerAccess.permissionsQuestion') }}
            </Label>
          </template>
          <div class="flex flex-wrap gap-4">
            <label
              v-for="permission in permissionOptions"
              :key="permission"
              class="flex items-center gap-2 text-xs cursor-pointer"
            >
              <Checkbox
                :model-value="formPermissions[permission]"
                @update:model-value="(v) => setFormPermission(permission, v === true)"
              />
              {{ permissionLabel(permission) }}
            </label>
          </div>
        </FieldStack>

        <div class="flex items-center justify-end gap-2 pt-1">
          <Button
            variant="ghost"
            size="sm"
            @click="closeForm"
          >
            {{ $t('common.cancel') }}
          </Button>
          <Button
            size="sm"
            :disabled="!canSubmit"
            :loading="isSaving"
            @click="handleCreate"
          >
            {{ $t('common.save') }}
          </Button>
        </div>
      </FormStack>
    </div>

    <InlineLoadingRow
      v-if="isLoading"
      size="md"
      surface="card-row"
    >
      {{ $t('common.loading') }}
    </InlineLoadingRow>

    <Empty
      v-else-if="grants.length === 0"
      class="py-12"
    >
      <EmptyHeader>
        <EmptyTitle>{{ $t('bots.access.peerAccess.title') }}</EmptyTitle>
        <EmptyDescription>{{ $t('bots.access.peerAccess.empty') }}</EmptyDescription>
      </EmptyHeader>
    </Empty>

    <template v-else>
      <SettingsRow
        v-for="grant in grants"
        :key="grant.id || grant.subject_type + (grant.subject_bot_id || 'any_bot')"
      >
        <template #leading>
          <Avatar class="size-7 shrink-0">
            <AvatarImage
              v-if="grant.subject_type === 'bot' && grant.subject_bot_avatar_url"
              :src="grant.subject_bot_avatar_url"
            />
            <AvatarFallback class="bg-muted text-muted-foreground">
              <Bot
                v-if="grant.subject_type === 'any_bot'"
                class="size-3.5"
              />
              <span
                v-else
                class="text-caption"
              >{{ initials(grant) }}</span>
            </AvatarFallback>
          </Avatar>
        </template>

        <template #content>
          <span class="truncate text-xs font-medium text-foreground">
            {{ grantLabel(grant) }}
          </span>
          <p
            v-if="grant.subject_type === 'bot' && grant.subject_bot_name"
            class="truncate text-xs text-muted-foreground"
          >
            @{{ grant.subject_bot_name }}
          </p>
        </template>

        <div class="flex items-center gap-3">
          <label
            v-for="permission in permissionOptions"
            :key="permission"
            class="flex items-center gap-1.5 text-xs cursor-pointer text-foreground"
          >
            <Checkbox
              :model-value="hasPerm(grant, permission)"
              :disabled="isRowBusy(grant)"
              @update:model-value="() => togglePerm(grant, permission)"
            />
            {{ permissionLabel(permission) }}
          </label>

          <ConfirmPopover
            :title="$t('bots.access.peerAccess.removeConfirm')"
            :cancel-text="$t('common.cancel')"
            :confirm-text="$t('common.confirm')"
            @confirm="() => handleDelete(grant)"
          >
            <template #trigger>
              <Button
                variant="ghost"
                size="icon-sm"
                class="text-muted-foreground"
                :disabled="isRowBusy(grant)"
              >
                <Trash2 class="size-3.5" />
              </Button>
            </template>
          </ConfirmPopover>
        </div>
      </SettingsRow>
    </template>
  </SettingsSection>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQuery, useQueryCache } from '@pinia/colada'
import { Plus, Trash2, Bot } from 'lucide-vue-next'
import {
  Button,
  Label,
  Checkbox,
  Avatar,
  AvatarImage,
  toast,
  AvatarFallback,
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
  SegmentedControl,
} from '@felinic/ui'
import { ConfirmPopover, FieldStack, FormStack, InlineLoadingRow, SettingsRow, SettingsSection } from '@felinic/ui'
import SearchableSelectPopover from '@/components/searchable-select-popover/index.vue'
import type { SearchableSelectOption } from '@/components/searchable-select-popover/index.vue'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { BOT_PEER_PERMISSION_ORDER, expandBotPeerPermissions, type BotPeerPermission } from '@/utils/bot-permissions'
import {
  getBotsByBotIdBotAccess,
  getBotsByBotIdBotAccessCandidates,
  postBotsByBotIdBotAccess,
  putBotsByBotIdBotAccessByGrantId,
  deleteBotsByBotIdBotAccessByGrantId,
} from '@memohai/sdk'
import type { BotsPeerGrant, HandlersBotPeerCandidate } from '@memohai/sdk'

type Permission = BotPeerPermission

const props = defineProps<{
  botId: string
}>()

const { t } = useI18n()
const queryCache = useQueryCache()

const { data: grantsData, isLoading } = useQuery({
  key: () => ['bot-peer-access', props.botId],
  query: async () => {
    const { data } = await getBotsByBotIdBotAccess({
      path: { bot_id: props.botId },
      throwOnError: true,
    })
    return data
  },
  enabled: () => !!props.botId,
})

const grants = computed<BotsPeerGrant[]>(() => grantsData.value?.items ?? [])

const formVisible = ref(false)
const formSubjectType = ref<'bot' | 'any_bot'>('bot')
const formSubjectBotId = ref('')
const formPermissions = reactive<Record<Permission, boolean>>({
  discover: true,
  contact: true,
  delegate: false,
})
const isSaving = ref(false)
const busyGrantIds = ref<Set<string>>(new Set())
const permissionOptions = BOT_PEER_PERMISSION_ORDER

// The candidates endpoint already excludes this bot, the bots the caller cannot
// manage, and the ones that are already granted, so whatever it returns is
// guaranteed to be accepted by the create call.
const { data: candidatesData } = useQuery({
  key: () => ['bot-peer-access-candidates', props.botId],
  query: async () => {
    const { data } = await getBotsByBotIdBotAccessCandidates({
      path: { bot_id: props.botId },
      query: { limit: 200 },
      throwOnError: true,
    })
    return data
  },
  enabled: () => !!props.botId && formVisible.value,
})

const candidateOptions = computed<SearchableSelectOption[]>(() =>
  (candidatesData.value?.items ?? [])
    .filter((c: HandlersBotPeerCandidate) => !!c.id)
    .map((c: HandlersBotPeerCandidate) => ({
      value: c.id ?? '',
      label: c.display_name || c.name || (c.id ?? ''),
      description: c.name ? `@${c.name}` : undefined,
      keywords: [c.name ?? '', c.display_name ?? ''],
    })),
)

const anyBotExists = computed(() => grants.value.some((g) => g.subject_type === 'any_bot'))
const subjectTypeItems = computed(() => [
  {
    value: 'bot',
    label: t('bots.access.peerAccess.specificBot'),
  },
  {
    value: 'any_bot',
    label: t('bots.access.peerAccess.anyBot'),
    disabled: anyBotExists.value,
  },
])

const canSubmit = computed(() => {
  if (buildPermissions().length === 0) return false
  if (formSubjectType.value === 'any_bot') return !anyBotExists.value
  return !!formSubjectBotId.value
})

function openAddForm() {
  formVisible.value = true
  formSubjectType.value = 'bot'
  formSubjectBotId.value = ''
  formPermissions.discover = true
  formPermissions.contact = true
  formPermissions.delegate = false
}

function closeForm() {
  formVisible.value = false
  formSubjectBotId.value = ''
}

function buildPermissions(): string[] {
  const selected = new Set<Permission>()
  for (const permission of permissionOptions) {
    if (formPermissions[permission]) selected.add(permission)
  }
  return normalizePermissionSelection(selected)
}

// The checkboxes mirror the delegate → contact → discover implication chain so
// the form can never present a state the backend would silently rewrite.
function setFormPermission(permission: Permission, checked: boolean) {
  formPermissions[permission] = checked
  if (checked) {
    for (const implied of expandBotPeerPermissions([permission])) formPermissions[implied] = true
    return
  }
  for (const scope of permissionOptions) {
    if (scope !== permission && formPermissions[scope] && expandBotPeerPermissions([scope]).includes(permission)) {
      formPermissions[scope] = false
    }
  }
}

function normalizePermissionSelection(selected: Set<Permission>): Permission[] {
  return expandBotPeerPermissions([...selected])
}

function permissionLabel(permission: Permission): string {
  switch (permission) {
    case 'discover': return t('bots.access.peerAccess.permissionDiscover')
    case 'contact': return t('bots.access.peerAccess.permissionContact')
    case 'delegate': return t('bots.access.peerAccess.permissionDelegate')
  }
}

function initials(grant: BotsPeerGrant): string {
  const name = grant.subject_bot_display_name || grant.subject_bot_name || '?'
  return name.trim().charAt(0).toUpperCase()
}

function grantLabel(grant: BotsPeerGrant): string {
  if (grant.subject_type === 'any_bot') return t('bots.access.peerAccess.anyBot')
  return grant.subject_bot_display_name || grant.subject_bot_name || grant.subject_bot_id || ''
}

function hasPerm(grant: BotsPeerGrant, perm: Permission): boolean {
  return expandBotPeerPermissions(grant.permissions).includes(perm)
}

function isRowBusy(grant: BotsPeerGrant): boolean {
  return !!grant.id && busyGrantIds.value.has(grant.id)
}

function invalidate() {
  return queryCache.invalidateQueries({ key: ['bot-peer-access', props.botId] })
}

async function handleCreate() {
  if (!canSubmit.value || isSaving.value) return
  isSaving.value = true
  try {
    await postBotsByBotIdBotAccess({
      path: { bot_id: props.botId },
      body: {
        subject_type: formSubjectType.value,
        subject_bot_id: formSubjectType.value === 'bot' ? formSubjectBotId.value : undefined,
        permissions: buildPermissions(),
      },
      throwOnError: true,
    })
    await invalidate()
    toast.success(t('bots.access.peerAccess.saved'))
    closeForm()
  }
  catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.access.peerAccess.saveFailed')))
  }
  finally {
    isSaving.value = false
  }
}

async function togglePerm(grant: BotsPeerGrant, perm: Permission) {
  if (!grant.id) return
  const current = new Set<Permission>(expandBotPeerPermissions(grant.permissions))
  if (current.has(perm)) {
    // Dropping a scope also drops everything that implies it, otherwise the
    // server would expand it straight back and the checkbox would not move.
    current.delete(perm)
    for (const scope of permissionOptions) {
      if (current.has(scope) && expandBotPeerPermissions([scope]).includes(perm)) current.delete(scope)
    }
  }
  else {
    current.add(perm)
  }
  const next = normalizePermissionSelection(current)
  if (next.length === 0) {
    toast.error(t('bots.access.peerAccess.atLeastOnePermission'))
    return
  }
  busyGrantIds.value.add(grant.id)
  try {
    await putBotsByBotIdBotAccessByGrantId({
      path: { bot_id: props.botId, grant_id: grant.id },
      body: { permissions: next },
      throwOnError: true,
    })
    await invalidate()
  }
  catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.access.peerAccess.saveFailed')))
  }
  finally {
    busyGrantIds.value.delete(grant.id)
  }
}

async function handleDelete(grant: BotsPeerGrant) {
  if (!grant.id) return
  busyGrantIds.value.add(grant.id)
  try {
    await deleteBotsByBotIdBotAccessByGrantId({
      path: { bot_id: props.botId, grant_id: grant.id },
      throwOnError: true,
    })
    await invalidate()
    toast.success(t('bots.access.peerAccess.removed'))
  }
  catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.access.peerAccess.removeFailed')))
  }
  finally {
    busyGrantIds.value.delete(grant.id)
  }
}
</script>
