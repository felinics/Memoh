export interface AttentionNotification {
  botId: string
  sessionId: string
  attentionAt: string
  title: string
  body: string
  /** The run notified the user: opening it scrolls to that notice. */
  notice?: boolean
  /** The session is a run of this schedule: opening it reveals the run in the schedule panel. */
  scheduleId?: string
}
