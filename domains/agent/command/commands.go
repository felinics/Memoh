package command

// buildRegistry constructs the full command registry with all resource groups.
func (h *Handler) buildRegistry() *Registry {
	r := newRegistry()
	r.RegisterGroup(h.buildScheduleGroup())
	r.RegisterGroup(h.buildMCPGroup())
	r.RegisterGroup(h.buildSettingsGroup())
	r.RegisterGroup(h.buildLanguageGroup())
	r.RegisterGroup(h.buildModelGroup())
	r.RegisterGroup(h.buildReasoningGroup())
	r.RegisterGroup(h.buildMemoryGroup())
	r.RegisterGroup(h.buildSearchGroup())
	r.RegisterGroup(h.buildUsageGroup())
	r.RegisterGroup(h.buildEmailGroup())
	r.RegisterGroup(h.buildHeartbeatGroup())
	r.RegisterGroup(h.buildSkillGroup())
	r.RegisterGroup(h.buildFSGroup())
	r.RegisterGroup(h.buildStatusGroup())
	r.RegisterGroup(h.buildContextGroup())
	r.RegisterGroup(h.buildAccessGroup())
	r.RegisterGroup(h.buildLinkGroup())
	r.RegisterGroup(h.buildCompactGroup())
	return r
}
