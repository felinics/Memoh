import { computed, type Ref } from 'vue'

interface BotStatusSource {
  status?: string | null
  is_active?: boolean | null
  check_state?: string | null
  check_issue_count?: number | null
}

export function useBotStatusMeta(
  bot: Ref<BotStatusSource | null | undefined>,
  t: (key: string, named?: Record<string, unknown>) => string,
) {
  const isCreating = computed(() => bot.value?.status === 'creating')
  const isDeleting = computed(() => bot.value?.status === 'deleting')
  // Workspace setup failed during creation and its resources were released.
  // The bot is not pending (the user can retry or delete it), but it is not
  // usable either, so it renders as a destructive lifecycle state.
  const isFailed = computed(() => bot.value?.status === 'failed')
  const isPending = computed(() => isCreating.value || isDeleting.value)
  const hasIssue = computed(() => bot.value?.check_state === 'issue')

  const issueTitle = computed(() => {
    const count = Number(bot.value?.check_issue_count ?? 0)
    if (count <= 0) {
      return t('bots.checks.hasIssue')
    }
    return t('bots.checks.issueCount', { count })
  })

  const statusVariant = computed<'default' | 'secondary' | 'destructive'>(() => {
    if (isPending.value) return 'secondary'
    if (isFailed.value || hasIssue.value) return 'destructive'
    return bot.value?.is_active ? 'default' : 'secondary'
  })

  const statusLabel = computed(() => {
    if (isCreating.value) return t('bots.lifecycle.creating')
    if (isDeleting.value) return t('bots.lifecycle.deleting')
    if (isFailed.value) return t('bots.lifecycle.createFailed')
    if (hasIssue.value) return issueTitle.value
    return bot.value?.is_active ? t('bots.active') : t('bots.inactive')
  })

  return {
    hasIssue,
    isFailed,
    isPending,
    issueTitle,
    statusLabel,
    statusVariant,
  }
}
