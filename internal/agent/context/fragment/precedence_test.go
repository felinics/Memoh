package contextfrag

import "testing"

func TestTrustRankOrdering(t *testing.T) {
	t.Parallel()

	if TrustRank(TrustExternal) >= TrustRank(TrustUser) ||
		TrustRank(TrustUser) >= TrustRank(TrustWorkspace) ||
		TrustRank(TrustWorkspace) >= TrustRank(TrustSystem) {
		t.Fatal("trust must rank external < user < workspace < system")
	}
	if TrustRank("") != TrustRank(TrustExternal) {
		t.Fatal("unknown trust ranks as external (least privileged)")
	}
}

func TestScopeSpecificityClosestWins(t *testing.T) {
	t.Parallel()

	global := Scope{}
	bot := Scope{BotID: "b"}
	chat := Scope{BotID: "b", ChatID: "c"}
	session := Scope{BotID: "b", ChatID: "c", SessionID: "s"}

	ranks := []int{
		global.SpecificityRank(),
		bot.SpecificityRank(),
		chat.SpecificityRank(),
		session.SpecificityRank(),
	}
	for i := 1; i < len(ranks); i++ {
		if ranks[i] <= ranks[i-1] {
			t.Fatalf("specificity must strictly increase global<bot<chat<session: %v", ranks)
		}
	}
}
