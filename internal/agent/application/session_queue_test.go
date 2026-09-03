package application

import (
	"context"
	"errors"
	"sync"
	"testing"

	sessionqueue "github.com/felinics/memoh/internal/agent/runtime/session/queue"
)

// TestQueueAdmissionGateRejectsBeyondLimit pins the backpressure contract: the
// gate is a non-blocking bound, so the request past the limit fails immediately
// with ErrAdmissionOverloaded rather than waiting, and releasing a slot admits
// the next caller.
func TestQueueAdmissionGateRejectsBeyondLimit(t *testing.T) {
	s := &Service{}
	s.SetQueueAdmissionLimit(2)

	release1, err := s.acquireQueueAdmission()
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	release2, err := s.acquireQueueAdmission()
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if _, err := s.acquireQueueAdmission(); !errors.Is(err, sessionqueue.ErrAdmissionOverloaded) {
		t.Fatalf("third acquire = %v, want ErrAdmissionOverloaded", err)
	}
	release1()
	release3, err := s.acquireQueueAdmission()
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	release2()
	release3()
}

func TestQueueAdmissionGateDefaultsLazilyAndIsConcurrencySafe(t *testing.T) {
	s := &Service{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted, rejected := 0, 0
	for i := 0; i < defaultQueueAdmissionLimit*3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := s.acquireQueueAdmission()
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				rejected++
				return
			}
			admitted++
			// Hold the slot so later goroutines observe a full gate.
			_ = release
		}()
	}
	wg.Wait()
	if admitted != defaultQueueAdmissionLimit {
		t.Fatalf("admitted = %d, want %d", admitted, defaultQueueAdmissionLimit)
	}
	if rejected != defaultQueueAdmissionLimit*2 {
		t.Fatalf("rejected = %d, want %d", rejected, defaultQueueAdmissionLimit*2)
	}
	_ = context.Background
}
