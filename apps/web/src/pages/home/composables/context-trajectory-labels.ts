export function captureStageLabel(stage: string | undefined, t: (key: string) => string, has: (key: string) => boolean): string {
  const key = `chat.trajectory.captureStages.${stage ?? ''}`
  return has(key) ? t(key) : stage || t('chat.trajectory.captureUnknownStage')
}
