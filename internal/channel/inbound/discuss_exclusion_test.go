package inbound

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/memohai/memoh/internal/channel"
)

type perBotTelegramDiscussPolicyReader map[string]TelegramDiscussPolicy

func (r perBotTelegramDiscussPolicyReader) TelegramDiscussPolicy(
	_ context.Context,
	botID string,
) (TelegramDiscussPolicy, error) {
	return r[botID], nil
}

func TestPassiveDiscussExclusionPreservesRatesAndEliminatesOverlap(t *testing.T) {
	t.Parallel()

	// With pA=pB=1/4, B is sampled at 1/3 only when A misses. This
	// 4-by-3 exact grid therefore has 3 A wins, 3 B wins, and 6 misses.
	firstDraws := []float64{0.125, 0.375, 0.625, 0.875}
	secondDraws := []float64{1.0 / 6, 3.0 / 6, 5.0 / 6}
	var firstWins, secondWins, misses int
	for _, firstDraw := range firstDraws {
		for _, secondDraw := range secondDraws {
			var sampler passiveDiscussExclusionSampler
			first, saturated := sampler.sample("message", "bot-a", 0.25, func() float64 { return firstDraw })
			if saturated {
				t.Fatal("first participant unexpectedly saturated")
			}
			second, saturated := sampler.sample("message", "bot-b", 0.25, func() float64 { return secondDraw })
			if saturated {
				t.Fatal("second participant unexpectedly saturated")
			}
			if first && second {
				t.Fatal("mutually exclusive participants both won")
			}
			switch {
			case first:
				firstWins++
			case second:
				secondWins++
			default:
				misses++
			}
		}
	}
	if firstWins != 3 || secondWins != 3 || misses != 6 {
		t.Fatalf("outcomes = first:%d second:%d miss:%d, want 3/3/6", firstWins, secondWins, misses)
	}
}

func TestShouldNotifyDiscussMutualExclusionCoordinatesBots(t *testing.T) {
	t.Parallel()

	draws := []float64{0.90, 0.20}
	drawIndex := 0
	processor := &ChannelInboundProcessor{
		telegramDiscussPolicy: perBotTelegramDiscussPolicyReader{
			"bot-a": {PassiveSampleRate: 0.25, PassiveMutualExclusion: true},
			"bot-b": {PassiveSampleRate: 0.25, PassiveMutualExclusion: true},
		},
		discussSample: func() float64 {
			value := draws[drawIndex]
			drawIndex++
			return value
		},
	}
	msg := channel.InboundMessage{
		Channel:     channel.ChannelTypeTelegram,
		ReplyTarget: "group-1",
		Message:     channel.Message{ID: "message-1", Text: "ordinary chatter"},
		Conversation: channel.Conversation{
			ID:   "group-1",
			Type: channel.ConversationTypeGroup,
		},
	}

	first, firstRate, _, _ := processor.shouldNotifyDiscuss(context.Background(), "team-1", "bot-a", msg, false)
	second, secondRate, _, _ := processor.shouldNotifyDiscuss(context.Background(), "team-1", "bot-b", msg, false)
	if first || !second {
		t.Fatalf("notifications = first:%v second:%v, want false/true", first, second)
	}
	if firstRate != 0.25 || secondRate != 0.25 {
		t.Fatalf("reported rates = %v/%v, want 0.25/0.25", firstRate, secondRate)
	}
	if drawIndex != 2 {
		t.Fatalf("random draws = %d, want 2", drawIndex)
	}
}

func TestPassiveDiscussExclusionCapsImpossibleProbabilitySum(t *testing.T) {
	t.Parallel()

	var sampler passiveDiscussExclusionSampler
	first, saturated := sampler.sample("message", "bot-a", 0.75, func() float64 { return 0.99 })
	if first || saturated {
		t.Fatalf("first sample = selected:%v saturated:%v, want false/false", first, saturated)
	}
	second, saturated := sampler.sample("message", "bot-b", 0.50, func() float64 { return 0 })
	if !second || !saturated {
		t.Fatalf("second sample = selected:%v saturated:%v, want true/true", second, saturated)
	}
}

func TestPassiveDiscussExclusionConcurrentAtMostOneWinner(t *testing.T) {
	t.Parallel()

	var sampler passiveDiscussExclusionSampler
	var winners atomic.Int32
	start := make(chan struct{})
	var group sync.WaitGroup
	for i := range 100 {
		group.Add(1)
		go func(participant string) {
			defer group.Done()
			<-start
			selected, _ := sampler.sample("message", participant, 0.01, func() float64 { return 0 })
			if selected {
				winners.Add(1)
			}
		}("bot-" + strconv.Itoa(i))
	}
	close(start)
	group.Wait()
	if got := winners.Load(); got != 1 {
		t.Fatalf("winners = %d, want 1", got)
	}
}

func TestTelegramPassiveExclusionKeyUsesSharedConversationMessage(t *testing.T) {
	t.Parallel()

	msg := channel.InboundMessage{
		Channel:     channel.ChannelTypeTelegram,
		ReplyTarget: "group-1",
		Message:     channel.Message{ID: "message-1"},
	}
	first := telegramPassiveExclusionKey("team-1", msg)
	second := telegramPassiveExclusionKey("team-1", msg)
	if first == "" || first != second {
		t.Fatalf("shared key = %q/%q", first, second)
	}
	msg.Message.ID = "message-2"
	if other := telegramPassiveExclusionKey("team-1", msg); other == first {
		t.Fatal("different source messages shared one exclusion key")
	}
}
