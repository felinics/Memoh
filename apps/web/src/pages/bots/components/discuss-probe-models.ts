interface ProbeModelLike {
  model_id?: string
  provider_id?: string | null
  enable?: boolean
  config?: {
    compatibilities?: string[] | null
  } | null
}

interface ProbeProviderLike {
  id?: string | null
  enable?: boolean
}

// The probe's only output is a forced `decide` tool call, so tool calling is a
// hard requirement rather than a preference: a model without it cannot return a
// verdict at all, and the gate would fail closed on every wake-up — a bot that
// silently never speaks. Offering such a model would be offering a mute switch.
export function filterDiscussProbeModels<T extends ProbeModelLike>(
  models: readonly T[],
  providers: readonly ProbeProviderLike[],
): T[] {
  const eligibleProviderIds = new Set(
    providers
      .filter(provider => provider.enable !== false)
      .map(provider => provider.id)
      .filter((id): id is string => Boolean(id)),
  )

  return models.filter((model) => {
    if (model.enable === false) return false
    if (!model.provider_id || !eligibleProviderIds.has(model.provider_id)) return false
    return (model.config?.compatibilities ?? []).includes('tool-call')
  })
}
