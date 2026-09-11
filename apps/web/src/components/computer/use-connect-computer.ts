import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMutation } from '@pinia/colada'
import { postUsersMeRuntimes, type UserruntimeRuntime } from '@memohai/sdk'
import { toast } from '@felinic/ui'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { useAccountRuntimes } from './use-computer-access'

// Shared one-click connect flow for every "add a computer" entry point:
// create the credential, hand it to the stepper dialog (command → connected
// → permissions), and let that dialog own cancellation cleanup. Used by the
// composer menu and the access dialog's zero-state ghost row; the Computers
// page keeps its own copy because it also drives the this-machine form.
export function useConnectComputer() {
  const { t } = useI18n()
  const { refetch: refetchRuntimes } = useAccountRuntimes()

  const open = ref(false)
  const credential = ref<UserruntimeRuntime | null>(null)
  const { mutateAsync: createRuntime, isLoading: creating } = useMutation({
    mutation: async () => (await postUsersMeRuntimes({ body: { name: '' }, throwOnError: true })).data,
  })

  async function startConnect(): Promise<void> {
    if (creating.value) return
    try {
      credential.value = await createRuntime()
      open.value = true
      void refetchRuntimes()
    } catch (error) {
      toast.error(resolveApiErrorMessage(error, t('runtimes.connectDialog.createFailed')))
    }
  }

  return { connectOpen: open, connectCredential: credential, creating, startConnect }
}
