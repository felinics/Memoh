<template>
  <SettingsSection>
    <SettingsRow
      :label="$t('bots.access.userAccess.title')"
      :description="$t('bots.access.userAccess.subtitle')"
    >
      <Button
        v-if="!formVisible"
        size="sm"
        variant="outline"
        class="shrink-0"
        @click="openAddForm"
      >
        <Plus class="size-4" />
        {{ $t('bots.access.userAccess.addMember') }}
      </Button>
    </SettingsRow>

    <!-- Inline add-member form: a one-off disclosure that opens in place inside
         the card. The border-b + py-4 band is the disclosure frame; its fields
         are the house form column (FormStack → FieldStack). -->
    <div
      v-if="formVisible"
      class="mx-4 border-b border-border py-4"
    >
      <FormStack>
        <!-- Muted question-style labels are this form's own voice, so they ride
             the #label slot to keep their exact styling rather than the default
             field label. -->
        <FieldStack>
          <template #label>
            <Label class="text-xs font-medium text-muted-foreground">
              {{ $t('bots.access.userAccess.subjectQuestion') }}
            </Label>
          </template>
          <SegmentedControl
            :model-value="formSubjectType"
            :items="subjectTypeItems"
            :aria-label="$t('bots.access.userAccess.subjectQuestion')"
            class="w-full sm:w-fit"
            @update:model-value="(value) => formSubjectType = value as 'user' | 'everyone'"
          />
        </FieldStack>

        <FieldStack v-if="formSubjectType === 'user'">
          <template #label>
            <Label class="text-xs font-medium text-muted-foreground">
              {{ $t('bots.access.userAccess.memberQuestion') }}
            </Label>
          </template>
          <SearchableSelectPopover
            v-model="formUserId"
            :options="candidateOptions"
            :placeholder="$t('bots.access.userAccess.selectMember')"
            :search-placeholder="$t('bots.access.userAccess.searchMember')"
            :empty-text="$t('bots.access.userAccess.noMemberCandidates')"
            :show-group-headers="false"
          />
        </FieldStack>

        <FieldStack>
          <template #label>
            <Label class="text-xs font-medium text-muted-foreground">
              {{ $t('bots.access.userAccess.permissionsQuestion') }}
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
        <EmptyTitle>{{ $t('bots.access.userAccess.title') }}</EmptyTitle>
        <EmptyDescription>{{ $t('bots.access.userAccess.empty') }}</EmptyDescription>
      </EmptyHeader>
    </Empty>

    <template v-else>
      <SettingsRow
        v-for="grant in grants"
        :key="grant.id || grant.subject_type + (grant.user_id || 'everyone')"
      >
        <template #leading>
          <Avatar class="size-7 shrink-0">
            <AvatarImage
              v-if="grant.subject_type === 'user' && grant.user_avatar_url"
              :src="grant.user_avatar_url"
            />
            <AvatarFallback class="bg-muted text-muted-foreground">
              <Globe
                v-if="grant.subject_type === 'everyone'"
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
          <div class="flex items-center gap-1.5">
            <span class="truncate text-xs font-medium text-foreground">
              {{ grantLabel(grant) }}
            </span>
            <Badge
              v-if="grant.is_owner"
              variant="secondary"
              size="sm"
            >
              {{ $t('bots.access.userAccess.ownerBadge') }}
            </Badge>
          </div>
          <p
            v-if="grant.subject_type === 'user' && grant.user_username"
            class="truncate text-xs text-muted-foreground"
          >
            @{{ grant.user_username }}
          </p>
        </template>

        <div class="flex items-center gap-3">
          <label
            v-for="permission in permissionOptions"
            :key="permission"
            class="flex items-center gap-1.5 text-xs"
            :class="grant.is_owner ? 'text-muted-foreground' : 'cursor-pointer text-foreground'"
          >
            <Checkbox
              :model-value="hasPerm(grant, permission)"
              :disabled="grant.is_owner || isRowBusy(grant)"
              @update:model-value="() => togglePerm(grant, permission)"
            />
            {{ permissionLabel(permission) }}
          </label>

          <ConfirmPopover
            v-if="!grant.is_owner"
            :title="$t('bots.access.userAccess.removeConfirm')"
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
import { Plus, Trash2, Globe } from 'lucide-vue-next'
import {
  Button,
  Label,
  Checkbox,
  Avatar,
  AvatarImage,
  toast,
  AvatarFallback,
  Badge,
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
import { BOT_PERMISSION_ORDER, expandBotPermissions, type BotPermission } from '@/utils/bot-permissions'
import {
  getBotsByBotIdUserAccess,
  getBotsByBotIdUserAccessCandidates,
  postBotsByBotIdUserAccess,
  putBotsByBotIdUserAccessByGrantId,
  deleteBotsByBotIdUserAccessByGrantId,
} from '@memohai/sdk'
import type { BotsUserGrant, HandlersBotUserCandidate } from '@memohai/sdk'

type Permission = BotPermission

const props = defineProps<{
  botId: string
}>()

const { t } = useI18n()
const queryCache = useQueryCache()

const { data: grantsData, isLoading } = useQuery({
  key: () => ['bot-user-access', props.botId],
  query: async () => {
    const { data } = await getBotsByBotIdUserAccess({
      path: { bot_id: props.botId },
      throwOnError: true,
    })
    return data
  },
  enabled: () => !!props.botId,
})

const grants = computed<BotsUserGrant[]>(() => grantsData.value?.items ?? [])

const formVisible = ref(false)
const formSubjectType = ref<'user' | 'everyone'>('user')
const formUserId = ref('')
const formPermissions = reactive<Record<Permission, boolean>>({
  chat: true,
  workspace_read: false,
  workspace_write: false,
  workspace_exec: false,
  manage: false,
})
const isSaving = ref(false)
const busyGrantIds = ref<Set<string>>(new Set())
const permissionOptions = BOT_PERMISSION_ORDER

const { data: candidatesData } = useQuery({
  key: () => ['bot-user-access-candidates', props.botId],
  query: async () => {
    const { data } = await getBotsByBotIdUserAccessCandidates({
      path: { bot_id: props.botId },
      query: { limit: 200 },
      throwOnError: true,
    })
    return data
  },
  enabled: () => !!props.botId && formVisible.value,
})

const candidateOptions = computed<SearchableSelectOption[]>(() => {
  const taken = new Set(
    grants.value
      .filter((g) => g.subject_type === 'user' && g.user_id)
      .map((g) => g.user_id as string),
  )
  return (candidatesData.value?.items ?? [])
    .filter((c: HandlersBotUserCandidate) => c.id && !taken.has(c.id))
    .map((c: HandlersBotUserCandidate) => ({
      value: c.id ?? '',
      label: c.display_name || c.username || (c.id ?? ''),
      description: c.username ? `@${c.username}` : undefined,
      keywords: [c.username ?? '', c.display_name ?? ''],
    }))
})

const everyoneExists = computed(() => grants.value.some((g) => g.subject_type === 'everyone'))
const subjectTypeItems = computed(() => [
  {
    value: 'user',
    label: t('bots.access.userAccess.specificMember'),
  },
  {
    value: 'everyone',
    label: t('bots.access.userAccess.everyone'),
    disabled: everyoneExists.value,
  },
])

const canSubmit = computed(() => {
  if (buildPermissions().length === 0) return false
  if (formSubjectType.value === 'everyone') return !everyoneExists.value
  return !!formUserId.value
})

function openAddForm() {
  formVisible.value = true
  formSubjectType.value = everyoneExists.value ? 'user' : 'user'
  formUserId.value = ''
  formPermissions.chat = true
  formPermissions.workspace_read = false
  formPermissions.workspace_write = false
  formPermissions.workspace_exec = false
  formPermissions.manage = false
}

function closeForm() {
  formVisible.value = false
  formUserId.value = ''
}

function buildPermissions(): string[] {
  const selected = new Set<Permission>()
  for (const permission of permissionOptions) {
    if (formPermissions[permission]) selected.add(permission)
  }
  return normalizePermissionSelection(selected)
}

function setFormPermission(permission: Permission, checked: boolean) {
  if (permission !== 'manage' && !checked && formPermissions.manage) {
    formPermissions.manage = false
  }
  formPermissions[permission] = checked
  if (permission === 'manage' && checked) {
    for (const item of permissionOptions) formPermissions[item] = true
  }
  if (permission === 'workspace_write' && checked) {
    formPermissions.workspace_read = true
  }
  if (permission === 'workspace_read' && !checked) {
    formPermissions.workspace_write = false
  }
}

function normalizePermissionSelection(selected: Set<Permission>): Permission[] {
  if (selected.has('manage')) {
    for (const permission of permissionOptions) selected.add(permission)
  }
  if (selected.has('workspace_write')) selected.add('workspace_read')
  return permissionOptions.filter(permission => selected.has(permission))
}

function permissionLabel(permission: Permission): string {
  switch (permission) {
    case 'chat': return t('bots.access.userAccess.permissionChat')
    case 'workspace_read': return t('bots.access.userAccess.permissionWorkspaceRead')
    case 'workspace_write': return t('bots.access.userAccess.permissionWorkspaceWrite')
    case 'workspace_exec': return t('bots.access.userAccess.permissionWorkspaceExec')
    case 'manage': return t('bots.access.userAccess.permissionManage')
  }
}

function initials(grant: BotsUserGrant): string {
  const name = grant.user_display_name || grant.user_username || '?'
  return name.trim().charAt(0).toUpperCase()
}

function grantLabel(grant: BotsUserGrant): string {
  if (grant.subject_type === 'everyone') return t('bots.access.userAccess.everyone')
  return grant.user_display_name || grant.user_username || grant.user_id || ''
}

function hasPerm(grant: BotsUserGrant, perm: Permission): boolean {
  return expandBotPermissions(grant.permissions).includes(perm)
}

function isRowBusy(grant: BotsUserGrant): boolean {
  return !!grant.id && busyGrantIds.value.has(grant.id)
}

function invalidate() {
  // Workspace Manage flows into Channel Members as inherited Manage, so refresh
  // the channel managers view too — otherwise the sibling tab shows a stale
  // inherited state after a grant change while runtime permissions already moved.
  return Promise.all([
    queryCache.invalidateQueries({ key: ['bot-user-access', props.botId] }),
    queryCache.invalidateQueries({ key: ['bot-channel-managers', props.botId] }),
  ])
}

async function handleCreate() {
  if (!canSubmit.value || isSaving.value) return
  isSaving.value = true
  try {
    await postBotsByBotIdUserAccess({
      path: { bot_id: props.botId },
      body: {
        subject_type: formSubjectType.value,
        user_id: formSubjectType.value === 'user' ? formUserId.value : undefined,
        permissions: buildPermissions(),
      },
      throwOnError: true,
    })
    await invalidate()
    toast.success(t('bots.access.userAccess.saved'))
    closeForm()
  }
  catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.access.userAccess.saveFailed')))
  }
  finally {
    isSaving.value = false
  }
}

async function togglePerm(grant: BotsUserGrant, perm: Permission) {
  if (grant.is_owner || !grant.id) return
  const current = new Set<Permission>(expandBotPermissions(grant.permissions))
  if (perm !== 'manage' && current.has('manage') && current.has(perm)) {
    current.delete('manage')
  }
  if (current.has(perm)) current.delete(perm)
  else current.add(perm)
  if (perm === 'workspace_read' && !current.has('workspace_read')) current.delete('workspace_write')
  const next = normalizePermissionSelection(current)
  if (next.length === 0) {
    toast.error(t('bots.access.userAccess.atLeastOnePermission'))
    return
  }
  busyGrantIds.value.add(grant.id)
  try {
    await putBotsByBotIdUserAccessByGrantId({
      path: { bot_id: props.botId, grant_id: grant.id },
      body: { permissions: next },
      throwOnError: true,
    })
    await invalidate()
  }
  catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.access.userAccess.saveFailed')))
  }
  finally {
    busyGrantIds.value.delete(grant.id)
  }
}

async function handleDelete(grant: BotsUserGrant) {
  if (grant.is_owner || !grant.id) return
  busyGrantIds.value.add(grant.id)
  try {
    await deleteBotsByBotIdUserAccessByGrantId({
      path: { bot_id: props.botId, grant_id: grant.id },
      throwOnError: true,
    })
    await invalidate()
    toast.success(t('bots.access.userAccess.removed'))
  }
  catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.access.userAccess.removeFailed')))
  }
  finally {
    busyGrantIds.value.delete(grant.id)
  }
}
</script>
