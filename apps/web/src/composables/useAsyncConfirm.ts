import { ref } from 'vue'

/**
 * A yes/no question answered through a confirm dialog, awaitable from code.
 * Bind `open` / `setOpen` to the dialog and call `answer(true)` from its
 * confirm action; any other way of closing it counts as no. Asking again
 * while a question is still open declines the earlier one.
 */
export function useAsyncConfirm() {
  const open = ref(false)
  let pending: ((answer: boolean) => void) | null = null

  function answer(value: boolean) {
    open.value = false
    const resolve = pending
    pending = null
    resolve?.(value)
  }

  function ask(): Promise<boolean> {
    pending?.(false)
    open.value = true
    return new Promise((resolve) => {
      pending = resolve
    })
  }

  function setOpen(value: boolean) {
    if (!value) answer(false)
  }

  return { open, ask, answer, setOpen }
}
