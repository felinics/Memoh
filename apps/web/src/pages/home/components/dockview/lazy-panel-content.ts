import { defineAsyncComponent, h, type Component } from 'vue'
import { withStylesheetRecovery } from '@/lib/chunk-load-error'
import PanelLoading from './panel-loading.vue'
import PanelLoadState from './panel-load-state.vue'

export const PanelLoadFailed = () => h(PanelLoading, { failed: true })
const PanelContentLoadFailed = () => h(PanelLoadState, { failed: true })

// A panel's heavy body (the editor, the terminal) sits behind its own async
// boundary, so the panel shell can be created for a restored background tab
// without downloading that body until the tab is first shown. The body gets the
// same loading and failed states as the panel itself, without a second panel
// frame around them: Retry remounts the whole panel (see lazyPanel in
// chat-workspace.vue), which runs this loader again.
export function lazyPanelContent<T extends Component>(load: () => Promise<T>): T {
  return defineAsyncComponent({
    loader: withStylesheetRecovery(load),
    loadingComponent: PanelLoadState,
    errorComponent: PanelContentLoadFailed,
  }) as T
}
