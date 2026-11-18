import { defineAsyncComponent, h, type Component } from 'vue'
import { withStylesheetRecovery } from '@/lib/chunk-load-error'
import WorkbenchPanelLoading from './workbench-panel-loading.vue'
import WorkbenchPanelLoadState from './workbench-panel-load-state.vue'

export const PanelLoadFailed = () => h(WorkbenchPanelLoading, { failed: true })
const PanelContentLoadFailed = () => h(WorkbenchPanelLoadState, { failed: true })

// A panel's heavy body (the editor, the schedule form) sits behind its own
// async boundary, so the panel shell can be created for a restored background
// tab without downloading that body until the tab is first shown. The body gets
// the same loading and failed states as the panel itself, without a second
// panel frame around them: Retry remounts the whole panel (see lazyPanel in
// workbench-stage.vue), which runs this loader again.
export function lazyPanelContent<T extends Component>(load: () => Promise<T>): T {
  return defineAsyncComponent({
    loader: withStylesheetRecovery(load),
    loadingComponent: WorkbenchPanelLoadState,
    errorComponent: PanelContentLoadFailed,
  }) as T
}
