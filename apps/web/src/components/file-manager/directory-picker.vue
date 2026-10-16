<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { getBotsByBotIdWorkdirsDirectories } from '@memohai/sdk'
import DirectoryPickerNode from './directory-picker-node.vue'
import type { DirectoryListing } from './directory-picker-types'
import { UserFacingError } from '@/utils/api-error'

// A directories-only tree for choosing ONE directory on a workspace target —
// the Cloud Computer or a connected computer. Same row shape and disclosure
// language as the Explorer tree (see ./tree-row), so a folder reads the same
// wherever it appears in the product — the picker is not a menu or a command
// palette and must not borrow their chrome.
//
// The caller supplies the bot, the target and optionally the root; everything
// else (listing, lazy expansion, per-node loading and retry) is owned here, so
// a host only needs `selectedPath` + `@select` and no layout of its own. Hosts
// key the picker by target so switching computers rebuilds the tree and no
// listing from the previous one can land in it.

const props = withDefaults(defineProps<{
  botId: string
  /** Workspace target to browse. */
  targetId?: string
  /**
   * Absolute path the tree is rooted at. Omit to start at the target's
   * default directory (a connected computer's workspace base).
   */
  rootPath?: string
  /** Label for the root row — the surface's name, not a directory name. */
  rootLabel: string
  /** Absolute path of the row drawn as selected. */
  selectedPath: string
}>(), {
  targetId: 'native',
  rootPath: '',
})

// Selection is reported, not owned: the host decides what a clicked directory
// means and feeds the highlight back through `selectedPath`, so the tree can
// also follow a selection the host derived from something else — e.g. the name
// or path the user typed.
defineEmits<{ select: [path: string] }>()

const { t } = useI18n()

async function listDirectory(path: string): Promise<DirectoryListing> {
  let result: Awaited<ReturnType<typeof getBotsByBotIdWorkdirsDirectories>>
  try {
    result = await getBotsByBotIdWorkdirsDirectories({
      path: { bot_id: props.botId },
      query: { workspace_target_id: props.targetId, path: path || undefined },
    })
  } catch {
    throw new UserFacingError(t('bots.folders.form.browseFailed'))
  }
  const { data, response } = result
  if (!data) {
    // The node shows this on its retry line. A computer that dropped offline
    // and a directory the runtime may not read are different fixes, so they
    // get different words; anything else stays generic.
    if (response?.status === 503) throw new UserFacingError(t('bots.folders.form.browseUnreachable'))
    if (response?.status === 403) throw new UserFacingError(t('bots.folders.form.browseForbidden'))
    throw new UserFacingError(t('bots.folders.form.browseFailed'))
  }
  return {
    path: data.path ?? path,
    directories: (data.directories ?? [])
      .filter(entry => (entry.name ?? '').trim() && entry.path && !(entry.name ?? '').startsWith('.'))
      .map(entry => ({ name: entry.name ?? '', path: entry.path ?? '' }))
      .sort((a, b) => a.name.localeCompare(b.name)),
  }
}
</script>

<template>
  <div class="overflow-hidden rounded-md border border-border">
    <div class="max-h-56 overflow-y-auto py-1">
      <DirectoryPickerNode
        :path="rootPath"
        :name="rootLabel"
        :depth="0"
        :selected-path="selectedPath"
        :list-directory="listDirectory"
        expand-on-mount
        @select="$emit('select', $event)"
      />
    </div>
  </div>
</template>
