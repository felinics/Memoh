import { ref } from 'vue'
import { defineStore } from 'pinia'

/** A run of a schedule that the schedule panel should reveal. */
export interface ScheduleRunFocus {
  scheduleId: string
  sessionId: string
}

/**
 * Where to look after the user opens a notification or a "Needs you" row: the
 * notice a run raised with notify_user, and, for a schedule's own run sessions,
 * that run in the schedule panel. Requests come only from the user's click and
 * are cleared by whoever honours them.
 */
export const useAttentionFocusStore = defineStore('attention-focus', () => {
  const noticeSessionId = ref<string | null>(null)
  const scheduleRun = ref<ScheduleRunFocus | null>(null)
  return { noticeSessionId, scheduleRun }
})
