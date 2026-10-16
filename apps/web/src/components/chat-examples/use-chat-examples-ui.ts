import { ref } from 'vue'
import { createGlobalState, useStorage } from '@vueuse/core'

/**
 * UI state shared by every usage-example surface. The gallery dialog is
 * mounted once in the app shell (MainSection), and any entry point opens it
 * through `openGallery`. Hiding the welcome suggestions is remembered on this
 * device; the composer's context row offers the way back while they are hidden.
 */
export const useChatExamplesUi = createGlobalState(() => {
  const galleryOpen = ref(false)
  const welcomeDismissed = useStorage('chat-examples-welcome-dismissed', false)

  function openGallery() {
    galleryOpen.value = true
  }

  return { galleryOpen, welcomeDismissed, openGallery }
})
