package feishu

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type fakeRegistrationAPI struct {
	mu      sync.Mutex
	begins  int
	polls   []string
	begin   func(n int) (RegistrationStart, error)
	results []fakePollResult
}

type fakePollResult struct {
	result RegistrationPollResult
	err    error
}

func (f *fakeRegistrationAPI) Begin(context.Context, RegistrationAppPreset) (RegistrationStart, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.begins++
	if f.begin != nil {
		return f.begin(f.begins)
	}
	return RegistrationStart{
		DeviceCode:      fmt.Sprintf("dc-%d", f.begins),
		VerificationURL: fmt.Sprintf("https://open.feishu.cn/page/launcher?user_code=%d", f.begins),
		Interval:        5 * time.Second,
		ExpiresIn:       10 * time.Minute,
	}, nil
}

func (f *fakeRegistrationAPI) Poll(_ context.Context, region, deviceCode string) (RegistrationPollResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.polls = append(f.polls, region+":"+deviceCode)
	if len(f.results) == 0 {
		return RegistrationPollResult{Status: RegistrationPending, Region: region}, nil
	}
	next := f.results[0]
	f.results = f.results[1:]
	return next.result, next.err
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newTestSessions(api *fakeRegistrationAPI) (*RegistrationSessions, *fakeClock) {
	clock := &fakeClock{now: time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)}
	n := 0
	return newRegistrationSessions(api, clock.Now, func() (string, error) {
		n++
		return fmt.Sprintf("session-%d", n), nil
	}), clock
}

func TestRegistrationSessionStartKeepsDeviceCodeServerSide(t *testing.T) {
	api := &fakeRegistrationAPI{}
	sessions, clock := newTestSessions(api)

	session, err := sessions.Start(context.Background(), "bot-1", "user-1", RegistrationAppPreset{})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if session.ID != "session-1" || session.VerificationURL == "" || session.Interval != 5*time.Second {
		t.Fatalf("session = %+v", session)
	}
	if !session.ExpiresAt.Equal(clock.now.Add(10 * time.Minute)) {
		t.Fatalf("expires at = %s", session.ExpiresAt)
	}
}

func TestRegistrationSessionPollIsThrottled(t *testing.T) {
	api := &fakeRegistrationAPI{}
	sessions, clock := newTestSessions(api)
	session, _ := sessions.Start(context.Background(), "bot-1", "user-1", RegistrationAppPreset{})

	result, err := sessions.Poll(context.Background(), session.ID, "bot-1", "user-1")
	if err != nil || result.Status != RegistrationPending {
		t.Fatalf("early poll = %+v, %v", result, err)
	}
	if len(api.polls) != 0 {
		t.Fatalf("early poll reached Feishu: %v", api.polls)
	}

	clock.Advance(5 * time.Second)
	if _, err := sessions.Poll(context.Background(), session.ID, "bot-1", "user-1"); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if _, err := sessions.Poll(context.Background(), session.ID, "bot-1", "user-1"); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(api.polls) != 1 || api.polls[0] != "feishu:dc-1" {
		t.Fatalf("polls = %v, want one feishu poll", api.polls)
	}
}

func TestRegistrationSessionRejectsOtherBotOrUser(t *testing.T) {
	api := &fakeRegistrationAPI{}
	sessions, clock := newTestSessions(api)
	session, _ := sessions.Start(context.Background(), "bot-1", "user-1", RegistrationAppPreset{})
	clock.Advance(5 * time.Second)

	for _, tc := range []struct{ bot, user string }{{"bot-2", "user-1"}, {"bot-1", "user-2"}} {
		result, err := sessions.Poll(context.Background(), session.ID, tc.bot, tc.user)
		if err != nil || result.Status != RegistrationExpired {
			t.Fatalf("poll as %s/%s = %+v, %v", tc.bot, tc.user, result, err)
		}
	}
	if len(api.polls) != 0 {
		t.Fatalf("foreign poll reached Feishu: %v", api.polls)
	}
}

func TestRegistrationSessionSuccessIsCollectableOnce(t *testing.T) {
	api := &fakeRegistrationAPI{results: []fakePollResult{{result: RegistrationPollResult{Status: RegistrationSuccess, Region: regionLark, AppID: "cli_1", AppSecret: "secret"}}}}
	sessions, clock := newTestSessions(api)
	session, _ := sessions.Start(context.Background(), "bot-1", "user-1", RegistrationAppPreset{})
	clock.Advance(5 * time.Second)

	result, err := sessions.Poll(context.Background(), session.ID, "bot-1", "user-1")
	if err != nil || result.Status != RegistrationSuccess || result.Region != regionLark {
		t.Fatalf("poll = %+v, %v", result, err)
	}
	clock.Advance(5 * time.Second)
	again, err := sessions.Poll(context.Background(), session.ID, "bot-1", "user-1")
	if err != nil || again.Status != RegistrationExpired || again.AppSecret != "" {
		t.Fatalf("second poll = %+v, %v", again, err)
	}
}

func TestRegistrationSessionFollowsRegionAndSlowDown(t *testing.T) {
	api := &fakeRegistrationAPI{results: []fakePollResult{{result: RegistrationPollResult{Status: RegistrationPending, Region: regionLark, SlowDown: true}}}}
	sessions, clock := newTestSessions(api)
	session, _ := sessions.Start(context.Background(), "bot-1", "user-1", RegistrationAppPreset{})
	clock.Advance(5 * time.Second)
	if _, err := sessions.Poll(context.Background(), session.ID, "bot-1", "user-1"); err != nil {
		t.Fatalf("poll: %v", err)
	}

	clock.Advance(5 * time.Second)
	if _, err := sessions.Poll(context.Background(), session.ID, "bot-1", "user-1"); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(api.polls) != 1 {
		t.Fatalf("slow_down did not widen the interval: %v", api.polls)
	}
	clock.Advance(5 * time.Second)
	if _, err := sessions.Poll(context.Background(), session.ID, "bot-1", "user-1"); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(api.polls) != 2 || api.polls[1] != "lark:dc-1" {
		t.Fatalf("polls = %v, want the second poll on lark", api.polls)
	}
}

func TestRegistrationSessionErrors(t *testing.T) {
	transient := errors.New("connection reset")
	api := &fakeRegistrationAPI{results: []fakePollResult{
		{err: transient},
		{err: fmt.Errorf("%w: poll: invalid_grant", ErrRegistrationFailed)},
	}}
	sessions, clock := newTestSessions(api)
	session, _ := sessions.Start(context.Background(), "bot-1", "user-1", RegistrationAppPreset{})

	clock.Advance(5 * time.Second)
	if _, err := sessions.Poll(context.Background(), session.ID, "bot-1", "user-1"); !errors.Is(err, transient) {
		t.Fatalf("err = %v, want transient", err)
	}
	clock.Advance(5 * time.Second)
	if _, err := sessions.Poll(context.Background(), session.ID, "bot-1", "user-1"); !errors.Is(err, ErrRegistrationFailed) {
		t.Fatalf("err = %v, want ErrRegistrationFailed", err)
	}
	clock.Advance(5 * time.Second)
	result, err := sessions.Poll(context.Background(), session.ID, "bot-1", "user-1")
	if err != nil || result.Status != RegistrationExpired {
		t.Fatalf("poll after terminal failure = %+v, %v", result, err)
	}
}

func TestRegistrationSessionExpires(t *testing.T) {
	api := &fakeRegistrationAPI{}
	sessions, clock := newTestSessions(api)
	session, _ := sessions.Start(context.Background(), "bot-1", "user-1", RegistrationAppPreset{})
	clock.Advance(10 * time.Minute)

	result, err := sessions.Poll(context.Background(), session.ID, "bot-1", "user-1")
	if err != nil || result.Status != RegistrationExpired {
		t.Fatalf("poll = %+v, %v", result, err)
	}
	if len(api.polls) != 0 {
		t.Fatalf("expired poll reached Feishu: %v", api.polls)
	}
}

func TestRegistrationSessionStartReplacesPrevious(t *testing.T) {
	api := &fakeRegistrationAPI{}
	sessions, clock := newTestSessions(api)
	first, _ := sessions.Start(context.Background(), "bot-1", "user-1", RegistrationAppPreset{})
	second, _ := sessions.Start(context.Background(), "bot-1", "user-1", RegistrationAppPreset{})
	clock.Advance(5 * time.Second)

	result, err := sessions.Poll(context.Background(), first.ID, "bot-1", "user-1")
	if err != nil || result.Status != RegistrationExpired {
		t.Fatalf("replaced session poll = %+v, %v", result, err)
	}
	if _, err := sessions.Poll(context.Background(), second.ID, "bot-1", "user-1"); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(api.polls) != 1 || api.polls[0] != "feishu:dc-2" {
		t.Fatalf("polls = %v", api.polls)
	}
}

func TestRegistrationSessionStartRateLimit(t *testing.T) {
	api := &fakeRegistrationAPI{}
	sessions, clock := newTestSessions(api)
	for i := range registrationStartsPerOwner {
		if _, err := sessions.Start(context.Background(), "bot-1", "user-1", RegistrationAppPreset{}); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
	}
	if _, err := sessions.Start(context.Background(), "bot-1", "user-1", RegistrationAppPreset{}); !errors.Is(err, ErrRegistrationRateLimited) {
		t.Fatalf("err = %v, want ErrRegistrationRateLimited", err)
	}
	if api.begins != registrationStartsPerOwner {
		t.Fatalf("begins = %d, the limited start must not reach Feishu", api.begins)
	}
	if _, err := sessions.Start(context.Background(), "bot-1", "user-2", RegistrationAppPreset{}); err != nil {
		t.Fatalf("other user start: %v", err)
	}
	clock.Advance(registrationStartWindow)
	if _, err := sessions.Start(context.Background(), "bot-1", "user-1", RegistrationAppPreset{}); err != nil {
		t.Fatalf("start after window: %v", err)
	}
}
