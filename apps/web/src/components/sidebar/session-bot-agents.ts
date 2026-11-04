import type { ComputedRef, InjectionKey } from 'vue'
import type { BotagentsBotAgent } from '@memohai/sdk'

// The sessions panel reads the bot's Agent instances once and every row names
// its Agent from them: a query per row would mount and unmount dozens of
// queries in one update whenever the bot changes.
export const SessionBotAgentsKey: InjectionKey<ComputedRef<BotagentsBotAgent[] | undefined>> = Symbol('session-bot-agents')
