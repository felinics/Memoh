package feishu

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

const (
	registrationStartWindow    = 10 * time.Minute
	registrationStartsPerOwner = 10
	registrationMaxSessions    = 1000
)

// ErrRegistrationRateLimited reports that a user started too many
// registrations recently, or that the process holds too many at once. Every
// start mints a device code at Feishu from this server's address, so the
// limit protects the shared egress address as much as this process.
var ErrRegistrationRateLimited = errors.New("feishu app registration rate limited")

type registrationAPI interface {
	Begin(ctx context.Context, preset RegistrationAppPreset) (RegistrationStart, error)
	Poll(ctx context.Context, region, deviceCode string) (RegistrationPollResult, error)
}

// RegistrationSession is what the browser learns about a registration. The
// device code never leaves the server: holding it is enough to collect the
// new app's secret.
type RegistrationSession struct {
	ID              string
	VerificationURL string
	ExpiresAt       time.Time
	Interval        time.Duration
}

type registrationSession struct {
	botID      string
	ownerID    string
	deviceCode string
	region     string
	interval   time.Duration
	expiresAt  time.Time
	nextPollAt time.Time
	polling    bool
}

// RegistrationSessions keeps in-flight registrations in process memory. The
// server runs as a single replica, and a registration lost to a restart only
// costs the user a new QR code.
type RegistrationSessions struct {
	api   registrationAPI
	now   func() time.Time
	newID func() (string, error)

	mu       sync.Mutex
	sessions map[string]*registrationSession
	starts   map[string][]time.Time
}

// NewRegistrationSessions creates a session store backed by the public
// Feishu and Lark accounts endpoints.
func NewRegistrationSessions() *RegistrationSessions {
	return newRegistrationSessions(NewRegistrationClient(), time.Now, randomRegistrationID)
}

func newRegistrationSessions(api registrationAPI, now func() time.Time, newID func() (string, error)) *RegistrationSessions {
	return &RegistrationSessions{
		api:      api,
		now:      now,
		newID:    newID,
		sessions: make(map[string]*registrationSession),
		starts:   make(map[string][]time.Time),
	}
}

// Start begins a registration for botID on behalf of ownerID. A new start
// replaces the owner's previous registration for the same bot.
func (s *RegistrationSessions) Start(ctx context.Context, botID, ownerID string, preset RegistrationAppPreset) (RegistrationSession, error) {
	if err := s.reserveStart(ownerID); err != nil {
		return RegistrationSession{}, err
	}
	id, err := s.newID()
	if err != nil {
		return RegistrationSession{}, err
	}
	begun, err := s.api.Begin(ctx, preset)
	if err != nil {
		return RegistrationSession{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneLocked(now)
	for existingID, existing := range s.sessions {
		if existing.botID == botID && existing.ownerID == ownerID {
			delete(s.sessions, existingID)
		}
	}
	if len(s.sessions) >= registrationMaxSessions {
		return RegistrationSession{}, ErrRegistrationRateLimited
	}
	session := &registrationSession{
		botID:      botID,
		ownerID:    ownerID,
		deviceCode: begun.DeviceCode,
		region:     regionFeishu,
		interval:   begun.Interval,
		expiresAt:  now.Add(begun.ExpiresIn),
		nextPollAt: now.Add(begun.Interval),
	}
	s.sessions[id] = session
	return RegistrationSession{
		ID:              id,
		VerificationURL: begun.VerificationURL,
		ExpiresAt:       session.expiresAt,
		Interval:        session.interval,
	}, nil
}

// Poll advances the registration identified by sessionID. A session that is
// unknown, belongs to another bot or user, or has run out of time answers
// expired, so a caller cannot probe for other users' registrations. A
// terminal result removes the session, which makes the credentials of a
// successful registration collectable exactly once.
//
// Polls arriving faster than the platform's interval, or while another poll
// of the same session is in flight, answer pending without calling Feishu.
func (s *RegistrationSessions) Poll(ctx context.Context, sessionID, botID, ownerID string) (RegistrationPollResult, error) {
	s.mu.Lock()
	now := s.now()
	session, ok := s.sessions[sessionID]
	if !ok || session.botID != botID || session.ownerID != ownerID {
		s.mu.Unlock()
		return RegistrationPollResult{Status: RegistrationExpired}, nil
	}
	if !now.Before(session.expiresAt) {
		delete(s.sessions, sessionID)
		s.mu.Unlock()
		return RegistrationPollResult{Status: RegistrationExpired, Region: session.region}, nil
	}
	if session.polling || now.Before(session.nextPollAt) {
		region := session.region
		s.mu.Unlock()
		return RegistrationPollResult{Status: RegistrationPending, Region: region}, nil
	}
	session.polling = true
	deviceCode, region := session.deviceCode, session.region
	s.mu.Unlock()

	result, err := s.api.Poll(ctx, region, deviceCode)

	s.mu.Lock()
	defer s.mu.Unlock()
	session.polling = false
	session.nextPollAt = s.now().Add(session.interval)
	if err != nil {
		if errors.Is(err, ErrRegistrationFailed) {
			delete(s.sessions, sessionID)
		}
		return RegistrationPollResult{}, err
	}
	session.region = result.Region
	if result.SlowDown {
		session.interval += registrationSlowDownStep
		session.nextPollAt = s.now().Add(session.interval)
	}
	if result.Status != RegistrationPending {
		delete(s.sessions, sessionID)
	}
	return result, nil
}

func (s *RegistrationSessions) reserveStart(ownerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneLocked(now)
	recent := s.starts[ownerID]
	if len(recent) >= registrationStartsPerOwner {
		return ErrRegistrationRateLimited
	}
	s.starts[ownerID] = append(recent, now)
	return nil
}

func (s *RegistrationSessions) pruneLocked(now time.Time) {
	for id, session := range s.sessions {
		if !now.Before(session.expiresAt) {
			delete(s.sessions, id)
		}
	}
	cutoff := now.Add(-registrationStartWindow)
	for ownerID, times := range s.starts {
		kept := times[:0]
		for _, at := range times {
			if at.After(cutoff) {
				kept = append(kept, at)
			}
		}
		if len(kept) == 0 {
			delete(s.starts, ownerID)
			continue
		}
		s.starts[ownerID] = kept
	}
}

func randomRegistrationID() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
