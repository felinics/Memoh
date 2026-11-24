import { defineAsyncComponent, defineComponent, h } from 'vue'
import { useI18n } from 'vue-i18n'
import { PanePlaceholder } from '@felinic/ui'
import { withStylesheetRecovery } from '@/lib/chunk-load-error'

function placeholder(messageKey: string, loading: boolean) {
  return defineComponent({
    setup() {
      const { t } = useI18n()
      return () => h(PanePlaceholder, { loading }, () => t(messageKey))
    },
  })
}

// The editor for places outside the workspace panels (dialogs), downloaded when
// it is first shown. A failed download is retried the next time the editor is
// mounted, that is, when its dialog is opened again.
export const LazyCodeEditor = defineAsyncComponent({
  loader: withStylesheetRecovery(() => import('./CodeEditor.vue').then(m => m.default)),
  loadingComponent: placeholder('common.loading', true),
  errorComponent: placeholder('common.loadFailed', false),
})
