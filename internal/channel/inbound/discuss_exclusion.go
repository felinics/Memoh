package inbound

import (
	"strings"
	"sync"
	"time"

	"github.com/memohai/memoh/internal/channel"
)

const telegramPassiveExclusionTTL = 5 * time.Minute

// passiveDiscussExclusionSampler coordinates passive Discuss sampling for bots
// that receive the same Telegram message in this Channel process. Its zero value
// is ready for use.
//
// If earlier participants with total probability c did not win, a new bot with
// configured probability p is sampled at p/(1-c). Its unconditional probability
// therefore remains (1-c)*p/(1-c) = p, while a stored winner prevents overlap.
// This preserves every bot's original marginal probability and makes the total
// participation probability the sum of rates, provided that sum is at most 1.
type passiveDiscussExclusionSampler struct {
	mu        sync.Mutex
	entries   map[string]*passiveDiscussExclusionEntry
	lastSweep time.Time
}

type passiveDiscussExclusionEntry struct {
	updatedAt       time.Time
	allocated       float64
	winner          string
	participantWins map[string]bool
}

func (s *passiveDiscussExclusionSampler) sample(
	key string,
	participant string,
	rate float64,
	draw func() float64,
) (selected bool, saturated bool) {
	if s == nil || key == "" || participant == "" || draw == nil {
		return false, false
	}
	if rate < 0 {
		rate = 0
	} else if rate > 1 {
		rate = 1
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.entries == nil {
		s.entries = make(map[string]*passiveDiscussExclusionEntry)
	}
	if s.lastSweep.IsZero() || now.Sub(s.lastSweep) >= telegramPassiveExclusionTTL {
		cutoff := now.Add(-telegramPassiveExclusionTTL)
		for entryKey, entry := range s.entries {
			if entry == nil || entry.updatedAt.Before(cutoff) {
				delete(s.entries, entryKey)
			}
		}
		s.lastSweep = now
	}

	entry := s.entries[key]
	if entry == nil {
		entry = &passiveDiscussExclusionEntry{participantWins: make(map[string]bool)}
		s.entries[key] = entry
	}
	entry.updatedAt = now
	if decision, ok := entry.participantWins[participant]; ok {
		return decision, false
	}
	if entry.winner != "" {
		entry.participantWins[participant] = false
		return false, false
	}

	remaining := 1 - entry.allocated
	if remaining <= 0 {
		entry.participantWins[participant] = false
		return false, rate > 0
	}
	effectiveRate := rate
	if effectiveRate > remaining {
		effectiveRate = remaining
		saturated = true
	}
	conditionalRate := effectiveRate / remaining
	selected = draw() < conditionalRate
	entry.allocated += effectiveRate
	entry.participantWins[participant] = selected
	if selected {
		entry.winner = participant
	}
	return selected, saturated
}

func telegramPassiveExclusionKey(teamID string, msg channel.InboundMessage) string {
	messageID := strings.TrimSpace(msg.Message.ID)
	target := strings.TrimSpace(msg.ReplyTarget)
	if target == "" {
		target = strings.TrimSpace(msg.Conversation.ID)
	}
	if messageID == "" || target == "" {
		return ""
	}
	return strings.Join([]string{
		strings.TrimSpace(teamID),
		msg.Channel.String(),
		target,
		messageID,
	}, "\x00")
}
