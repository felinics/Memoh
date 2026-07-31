<template>
  <PageShell :title="t('people.title')">
    <template #actions>
      <Button
        type="button"
        @click="openCreateDialog"
      >
        <UserPlus class="size-4" />
        {{ t('people.newMember') }}
      </Button>
    </template>

    <div class="space-y-8">
      <Alert
        v-if="loadError"
        variant="destructive"
      >
        <AlertTitle>{{ t('people.loadFailed') }}</AlertTitle>
        <AlertDescription>{{ loadError }}</AlertDescription>
      </Alert>

      <section class="space-y-2.5">
        <h2 class="px-2 text-label font-medium text-muted-foreground">
          {{ membersTitle }}
        </h2>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{{ t('people.member') }}</TableHead>
              <TableHead>{{ t('people.role') }}</TableHead>
              <TableHead>{{ t('common.status') }}</TableHead>
              <TableHead>{{ t('people.lastLogin') }}</TableHead>
              <TableHead class="w-24 text-right">
                {{ t('common.actions') }}
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-if="loading && users.length === 0">
              <TableCell
                :colspan="5"
                class="p-0"
              >
                <div class="space-y-2 p-3">
                  <Skeleton
                    v-for="index in 5"
                    :key="index"
                    class="h-10 w-full"
                  />
                </div>
              </TableCell>
            </TableRow>
            <TableRow v-else-if="users.length === 0">
              <TableCell
                :colspan="5"
                class="h-28 text-center text-muted-foreground"
              >
                {{ t('people.empty') }}
              </TableCell>
            </TableRow>
            <TableRow
              v-for="user in users"
              v-else
              :key="user.id"
            >
              <TableCell>
                <div class="flex items-center gap-3">
                  <Avatar class="size-9 shrink-0">
                    <AvatarImage
                      v-if="user.avatar_url"
                      :src="user.avatar_url"
                      :alt="memberName(user)"
                    />
                    <AvatarFallback>{{ memberInitials(user) }}</AvatarFallback>
                  </Avatar>
                  <div class="min-w-0">
                    <div class="flex items-center gap-2">
                      <p class="truncate text-xs font-medium">
                        {{ memberName(user) }}
                      </p>
                      <Badge
                        v-if="isSelf(user)"
                        variant="outline"
                        size="sm"
                      >
                        {{ t('people.you') }}
                      </Badge>
                    </div>
                    <p class="mt-0.5 truncate text-[11px] text-muted-foreground">
                      @{{ user.username }}
                      <span v-if="user.email"> · {{ user.email }}</span>
                    </p>
                  </div>
                </div>
              </TableCell>
              <TableCell>
                <Badge
                  variant="outline"
                  size="sm"
                >
                  {{ roleLabel(user.role) }}
                </Badge>
              </TableCell>
              <TableCell>
                <div class="flex items-center gap-2">
                  <Switch
                    :model-value="!!user.is_active"
                    :disabled="isSelf(user) || isUserPending(user)"
                    :aria-label="t('people.toggleStatus', { name: memberName(user) })"
                    @update:model-value="(value) => updateUserStatus(user, !!value)"
                  />
                  <span class="text-[11px] text-muted-foreground">
                    {{ user.is_active ? t('common.active') : t('common.inactive') }}
                  </span>
                </div>
              </TableCell>
              <TableCell class="text-muted-foreground">
                {{ formatMemberDate(user.last_login_at, t('people.never')) }}
              </TableCell>
              <TableCell>
                <div class="flex justify-end gap-1">
                  <ConfirmPopover
                    :title="t('people.removeMember')"
                    :message="t('people.removeConfirm', { name: memberName(user) })"
                    :cancel-text="t('common.cancel')"
                    :confirm-text="t('people.removeMember')"
                    variant="destructive"
                    :loading="isUserPending(user)"
                    @confirm="removeMember(user)"
                  >
                    <template #trigger>
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon-sm"
                        :title="t('people.removeMember')"
                        :aria-label="t('people.removeMemberFor', { name: memberName(user) })"
                        :disabled="isSelf(user) || isUserPending(user)"
                      >
                        <Trash2 class="size-4" />
                      </Button>
                    </template>
                  </ConfirmPopover>
                </div>
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </section>
    </div>

    <Dialog v-model:open="createDialogOpen">
      <DialogContent
        class="sm:max-w-xl"
        :aria-describedby="undefined"
      >
        <DialogHeader>
          <DialogTitle>{{ t('people.newMember') }}</DialogTitle>
        </DialogHeader>

        <FormStack>
          <FieldStack
            :label="t('auth.username')"
            for="people-create-username"
          >
            <Input
              id="people-create-username"
              v-model="createForm.username"
              autocomplete="off"
              :placeholder="t('people.usernamePlaceholder')"
            />
          </FieldStack>
          <div class="grid gap-2 sm:grid-cols-2">
            <FieldStack
              :label="t('settings.displayName')"
              for="people-create-display-name"
            >
              <Input
                id="people-create-display-name"
                v-model="createForm.displayName"
                autocomplete="off"
                :placeholder="t('people.displayNamePlaceholder')"
              />
            </FieldStack>
            <FieldStack
              :label="t('people.email')"
              for="people-create-email"
            >
              <Input
                id="people-create-email"
                v-model="createForm.email"
                type="email"
                autocomplete="off"
                :placeholder="t('people.emailPlaceholder')"
              />
            </FieldStack>
          </div>
          <div class="grid gap-2 sm:grid-cols-2">
            <FieldStack
              :label="t('auth.password')"
              for="people-create-password"
            >
              <Input
                id="people-create-password"
                v-model="createForm.password"
                type="password"
                autocomplete="new-password"
              />
            </FieldStack>
            <FieldStack
              :label="t('settings.confirmPassword')"
              for="people-create-confirm-password"
            >
              <Input
                id="people-create-confirm-password"
                v-model="createForm.confirmPassword"
                type="password"
                autocomplete="new-password"
              />
            </FieldStack>
          </div>
          <FieldStack
            class="w-fit"
            :label="t('people.role')"
            for="people-create-role"
          >
            <Select v-model="createForm.role">
              <SelectTrigger id="people-create-role">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="member">
                  {{ t('people.roles.member') }}
                </SelectItem>
                <SelectItem value="admin">
                  {{ t('people.roles.admin') }}
                </SelectItem>
              </SelectContent>
            </Select>
          </FieldStack>
          <div class="flex items-center justify-between gap-4 border-t pt-4">
            <div class="space-y-0.5">
              <Label>{{ t('people.activeOnCreate') }}</Label>
              <p class="text-[11px] text-muted-foreground">
                {{ t('people.activeOnCreateHint') }}
              </p>
            </div>
            <Switch
              v-model="createForm.isActive"
              class="shrink-0"
            />
          </div>
        </FormStack>

        <DialogFooter class="mt-2">
          <Button
            type="button"
            variant="outline"
            :disabled="creating"
            @click="createDialogOpen = false"
          >
            {{ t('common.cancel') }}
          </Button>
          <Button
            type="button"
            :loading="creating"
            @click="createUser"
          >
            {{ t('common.create') }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </PageShell>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ConfirmPopover, FieldStack, FormStack, PageShell, toast } from '@felinic/ui'
import { Trash2, UserPlus } from 'lucide-vue-next'
import {
  Alert,
  AlertDescription,
  AlertTitle,
  Avatar,
  AvatarFallback,
  AvatarImage,
  Badge,
  Button,
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Input,
  Label,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Skeleton,
  Switch,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@felinic/ui'
import { useUserStore } from '@/store/user'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { formatDateTime } from '@/utils/date-time'
import {
  deleteUsersById,
  getUsers,
  postUsers,
  putUsersById,
} from '@memohai/sdk'
import type {
  AccountsAccount,
  AccountsCreateAccountRequest,
  AccountsUpdateAccountRequest,
} from '@memohai/sdk'

const { t } = useI18n()
const userStore = useUserStore()

type MemberRole = 'admin' | 'member'
type UserAccount = AccountsAccount

const users = ref<UserAccount[]>([])
const loading = ref(false)
const creating = ref(false)
const loadError = ref('')
const pendingUserIds = ref<Set<string>>(new Set())

const createDialogOpen = ref(false)

const createForm = reactive({
  username: '',
  displayName: '',
  email: '',
  role: 'member' as MemberRole,
  isActive: true,
  password: '',
  confirmPassword: '',
})

const membersTitle = computed(() =>
  users.value.length
    ? `${t('people.members')} · ${users.value.length}`
    : t('people.members'),
)
const currentUserId = computed(() => userStore.userInfo.id)

onMounted(() => {
  void loadUsers()
})

async function loadUsers() {
  loading.value = true
  loadError.value = ''
  try {
    const { data } = await getUsers({ throwOnError: true })
    users.value = data.items ?? []
  } catch (error) {
    loadError.value = resolveApiErrorMessage(error, t('people.loadFailed'), { prefixFallback: true })
    toast.error(loadError.value)
  } finally {
    loading.value = false
  }
}

function normalizeRole(role: string | undefined): MemberRole {
  return role === 'admin' ? 'admin' : 'member'
}

function roleLabel(role: string | undefined): string {
  return t(`people.roles.${normalizeRole(role)}`)
}

function memberName(user: UserAccount): string {
  return (user.display_name || user.username || user.id || '').trim()
}

function memberInitials(user: UserAccount): string {
  const value = memberName(user) || 'U'
  return value.slice(0, 2).toUpperCase()
}

function isSelf(user: UserAccount): boolean {
  return !!user.id && user.id === currentUserId.value
}

function isUserPending(user: UserAccount): boolean {
  return !!user.id && pendingUserIds.value.has(user.id)
}

function setUserPending(userID: string, pending: boolean) {
  const next = new Set(pendingUserIds.value)
  if (pending) {
    next.add(userID)
  } else {
    next.delete(userID)
  }
  pendingUserIds.value = next
}

function formatMemberDate(value: string | undefined, fallback: string): string {
  return formatDateTime(value, { fallback })
}

function resetCreateForm() {
  createForm.username = ''
  createForm.displayName = ''
  createForm.email = ''
  createForm.role = 'member'
  createForm.isActive = true
  createForm.password = ''
  createForm.confirmPassword = ''
}

function openCreateDialog() {
  resetCreateForm()
  createDialogOpen.value = true
}

function validatePasswordPair(password: string, confirmPassword: string): boolean {
  if (!password.trim()) {
    toast.error(t('settings.passwordRequired'))
    return false
  }
  if (password !== confirmPassword) {
    toast.error(t('settings.passwordNotMatch'))
    return false
  }
  return true
}

async function createUser() {
  const username = createForm.username.trim()
  const password = createForm.password.trim()
  const confirmPassword = createForm.confirmPassword.trim()
  if (!username) {
    toast.error(t('people.usernameRequired'))
    return
  }
  if (!validatePasswordPair(password, confirmPassword)) return

  creating.value = true
  try {
    const body: AccountsCreateAccountRequest = {
      username,
      password,
      display_name: createForm.displayName.trim() || undefined,
      email: createForm.email.trim() || undefined,
      role: createForm.role,
      is_active: createForm.isActive,
    }
    await postUsers({ body, throwOnError: true })
    toast.success(t('people.createSuccess'))
    createDialogOpen.value = false
    resetCreateForm()
    await loadUsers()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('people.createFailed'), { prefixFallback: true }))
  } finally {
    creating.value = false
  }
}

async function updateUserStatus(user: UserAccount, isActive: boolean) {
  const userID = user.id
  if (!userID || isSelf(user)) return

  setUserPending(userID, true)
  try {
    const body: AccountsUpdateAccountRequest = {
      role: normalizeRole(user.role),
      is_active: isActive,
    }
    await putUsersById({
      path: { id: userID },
      body,
      throwOnError: true,
    })
    toast.success(isActive ? t('people.enableSuccess') : t('people.disableSuccess'))
    await loadUsers()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('people.statusUpdateFailed'), { prefixFallback: true }))
    await loadUsers()
  } finally {
    setUserPending(userID, false)
  }
}

async function removeMember(user: UserAccount) {
  const userID = user.id
  if (!userID || isSelf(user)) return

  setUserPending(userID, true)
  try {
    await deleteUsersById({
      path: { id: userID },
      throwOnError: true,
    })
    toast.success(t('people.removeSuccess'))
    await loadUsers()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('people.removeFailed'), { prefixFallback: true }))
  } finally {
    setUserPending(userID, false)
  }
}

</script>
