package contextfrag

// TrustRank orders trust levels for adjudication: external content is the
// least privileged, system-authored content the most. Unknown levels rank as
// external so unlabelled sources never gain authority by omission.
func TrustRank(trust TrustLevel) int {
	switch trust {
	case TrustSystem:
		return 3
	case TrustWorkspace:
		return 2
	case TrustUser:
		return 1
	default:
		return 0
	}
}

// SpecificityRank scores how narrowly a scope binds, for closest-wins
// precedence: a session-scoped claim supersedes a chat-scoped one, which
// supersedes a bot-wide one, which supersedes a global one.
func (s Scope) SpecificityRank() int {
	rank := 0
	if s.BotID != "" {
		rank++
	}
	if s.ChatID != "" {
		rank += 2
	}
	if s.SessionID != "" {
		rank += 4
	}
	return rank
}
