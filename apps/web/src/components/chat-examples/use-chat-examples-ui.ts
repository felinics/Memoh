import { computed, ref } from 'vue'
import { createGlobalState, useStorage } from '@vueuse/core'
import { toast } from '@felinic/ui'
import i18n from '@/i18n'
import { useUserStore } from '@/store/user'

/**
 * UI state shared by every usage-example surface. The gallery dialog is
 * mounted once in the app shell (MainSection), and any entry point opens it
 * through `openGallery`. Hiding the welcome suggestions is remembered on this
 * device; the composer's context row offers the way back while they are hidden.
 * Hiding the sidebar entry is saved on the account, so it stays hidden on every
 * device; the Appearance page's switch is the way back. A failed save puts the
 * entry back and says so; `setSidebarEntryHidden` also resolves to whether the
 * save went through, for callers that announced the change optimistically.
 */
export const useChatExamplesUi = createGlobalState(() => {
  const userStore = useUserStore()
  const galleryOpen = ref(false)
  const welcomeDismissed = useStorage('chat-examples-welcome-dismissed', false)

  async function setSidebarEntryHidden(hidden: boolean): Promise<boolean> {
    try {
      await userStore.setChatExamplesSidebarEntryHidden(hidden)
      return true
    } catch {
      toast.error(i18n.global.t('chatExamples.sidebarEntrySaveFailed'))
      return false
    }
  }

  const sidebarEntryHidden = computed({
    get: () => userStore.chatExamplesSidebarEntryHidden,
    set: (hidden: boolean) => { void setSidebarEntryHidden(hidden) },
  })

  function openGallery() {
    galleryOpen.value = true
  }

  return { galleryOpen, welcomeDismissed, sidebarEntryHidden, setSidebarEntryHidden, openGallery }
})
