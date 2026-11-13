import { useDocumentVisibility, useIntervalFn } from '@vueuse/core'
import {
  computed,
  getCurrentInstance,
  getCurrentScope,
  onActivated,
  onDeactivated,
  onScopeDispose,
  readonly,
  ref,
  toValue,
  watch,
  type MaybeRefOrGetter,
  type Ref,
} from 'vue'

export interface UseVisiblePollOptions {
  enabled?: MaybeRefOrGetter<boolean>
  immediate?: boolean
  immediateOnResume?: boolean
}

function isPromiseLike(value: unknown): value is PromiseLike<unknown> {
  return (typeof value === 'object' || typeof value === 'function')
    && value !== null
    && typeof (value as PromiseLike<unknown>).then === 'function'
}

export function useVisiblePoll(
  fn: () => unknown | Promise<unknown>,
  intervalMs: number,
  options?: UseVisiblePollOptions,
): {
  isActive: Readonly<Ref<boolean>>
  pause: () => void
  resume: () => void
  trigger: () => Promise<void>
} {
  const visibility = useDocumentVisibility()
  const isDeactivated = ref(false)
  const manuallyPaused = ref(false)
  let disposed = false
  let inFlight: Promise<void> | null = null

  if (getCurrentInstance()) {
    onActivated(() => {
      isDeactivated.value = false
    })
    onDeactivated(() => {
      isDeactivated.value = true
    })
  }

  const isForeground = computed(() => visibility.value === 'visible' && !isDeactivated.value)
  const isEnabled = computed(() => Boolean(toValue(options?.enabled ?? true)))
  const shouldPoll = computed(() => !manuallyPaused.value && isForeground.value && isEnabled.value)

  function trigger(): Promise<void> {
    if (disposed) return Promise.resolve()
    if (inFlight) return inFlight

    let result: unknown
    try {
      result = fn()
    } catch (error) {
      return Promise.reject(error)
    }

    if (!isPromiseLike(result)) {
      return Promise.resolve()
    }

    const tracked = Promise.resolve(result)
      .then(() => undefined)
      .finally(() => {
        if (inFlight === tracked) {
          inFlight = null
        }
      })

    inFlight = tracked
    return tracked
  }

  const {
    isActive,
    pause: pauseInterval,
    resume: resumeInterval,
  } = useIntervalFn(
    () => {
      if (!shouldPoll.value) return
      void trigger().catch(() => {})
    },
    intervalMs,
    {
      immediate: shouldPoll.value,
      immediateCallback: false,
    },
  )

  if (options?.immediate && shouldPoll.value) {
    void trigger().catch(() => {})
  }

  watch(
    [shouldPoll, isForeground, isEnabled],
    ([canPoll, foreground, enabled], [prevCanPoll, prevForeground, prevEnabled]) => {
      if (!canPoll) {
        pauseInterval()
        return
      }

      if (!prevCanPoll) {
        resumeInterval()
      }

      const resumedForeground = foreground && prevForeground === false
      const enabledTurnedOn = enabled && prevEnabled === false

      if (resumedForeground && (options?.immediateOnResume ?? true)) {
        void trigger().catch(() => {})
      } else if (enabledTurnedOn && options?.immediate) {
        void trigger().catch(() => {})
      }
    },
    { flush: 'sync' },
  )

  function pause(): void {
    manuallyPaused.value = true
    pauseInterval()
  }

  function resume(): void {
    if (disposed) return
    manuallyPaused.value = false
  }

  if (getCurrentScope()) {
    onScopeDispose(() => {
      disposed = true
      pauseInterval()
    })
  }

  return {
    isActive: readonly(isActive),
    pause,
    resume,
    trigger,
  }
}
