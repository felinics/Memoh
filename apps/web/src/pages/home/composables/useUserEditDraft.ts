import type { InjectionKey, Ref } from 'vue'
import { inject, provide, ref } from 'vue'

// The in-place edit of the latest prompt, kept by the chat pane rather than by
// the MessageItem that shows it. A MessageItem remounts whenever its message is
// re-identified — a history refresh replaces optimistic and runtime ids with
// persisted ones, including the refresh that follows a failed save — and
// editor state held only in the component would vanish with it. The turn id
// names the round across those re-identifications, so a remounted item finds
// its open editor and draft here. Only the latest prompt is editable, so one
// slot is enough.
export interface UserEditDraft {
  turnId: string
  draft: string
}

const KEY: InjectionKey<Ref<UserEditDraft | null>> = Symbol('user-edit-draft')

export function provideUserEditDraft(): Ref<UserEditDraft | null> {
  const draft = ref<UserEditDraft | null>(null)
  provide(KEY, draft)
  return draft
}

// Null outside a chat pane: the MessageItem then keeps its edit state to itself.
export function injectUserEditDraft(): Ref<UserEditDraft | null> | null {
  return inject(KEY, null)
}
