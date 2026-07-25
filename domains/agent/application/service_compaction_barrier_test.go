package application

import (
	"testing"
	"time"
)

func TestSessionCompactionWaitsForEveryOutboundAssetLinker(t *testing.T) {
	t.Parallel()

	resolver := &Service{}
	releaseFirst := resolver.DeferSessionCompaction("bot-1", "session-1", "stream-1")
	releaseSecond := resolver.DeferSessionCompaction("bot-1", "session-1", "stream-2")
	compactionEntered := make(chan struct{})
	releaseCompaction := make(chan struct{})
	go func() {
		done := resolver.enterSessionCompaction("bot-1", "session-1")
		close(compactionEntered)
		<-releaseCompaction
		done()
	}()

	releaseFirst()
	assertChannelBlocked(t, compactionEntered, "compaction started while one asset linker remained")
	releaseSecond()
	assertChannelReady(t, compactionEntered, "compaction did not start after every asset linker finished")
	close(releaseCompaction)
}

func TestWaitingSessionCompactionBlocksLaterAssetLinkers(t *testing.T) {
	t.Parallel()

	resolver := &Service{}
	releaseFirst := resolver.DeferSessionCompaction("bot-1", "session-1", "stream-1")
	compactionEntered := make(chan struct{})
	releaseCompaction := make(chan struct{})
	go func() {
		done := resolver.enterSessionCompaction("bot-1", "session-1")
		close(compactionEntered)
		<-releaseCompaction
		done()
	}()
	assertChannelBlocked(t, compactionEntered, "compaction ignored an active asset linker")

	secondEntered := make(chan func(), 1)
	go func() {
		secondEntered <- resolver.DeferSessionCompaction("bot-1", "session-1", "stream-2")
	}()
	releaseFirst()
	assertChannelReady(t, compactionEntered, "waiting compaction did not acquire the session gate")
	assertChannelBlocked(t, secondEntered, "later asset linker bypassed waiting compaction")
	close(releaseCompaction)

	var releaseSecond func()
	select {
	case releaseSecond = <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("later asset linker did not acquire the released session gate")
	}
	releaseSecond()
}

func TestSynchronousSessionCompactionSuspendsOnlyItsOwnAssetLinker(t *testing.T) {
	t.Parallel()

	resolver := &Service{}
	releaseCurrent := resolver.DeferSessionCompaction("bot-1", "session-1", "stream-current")
	releaseOther := resolver.DeferSessionCompaction("bot-1", "session-1", "stream-other")
	compactionEntered := make(chan func(), 1)
	go func() {
		compactionEntered <- resolver.enterSessionCompactionForStream("bot-1", "session-1", "stream-current")
	}()

	assertChannelBlocked(t, compactionEntered, "sync compaction ignored another stream's asset linker")
	releaseOther()
	var finishCompaction func()
	select {
	case finishCompaction = <-compactionEntered:
	case <-time.After(time.Second):
		t.Fatal("sync compaction did not suspend its own asset linker")
	}
	finishCompaction()

	asyncEntered := make(chan struct{})
	go func() {
		done := resolver.enterSessionCompaction("bot-1", "session-1")
		close(asyncEntered)
		done()
	}()
	assertChannelBlocked(t, asyncEntered, "sync compaction did not restore its stream's asset linker")
	releaseCurrent()
	assertChannelReady(t, asyncEntered, "restored asset linker did not release async compaction")
}

func TestConcurrentSynchronousSessionCompactionsDoNotDeadlockUpgradingReaders(t *testing.T) {
	t.Parallel()

	resolver := &Service{}
	releaseFirst := resolver.DeferSessionCompaction("bot-1", "session-1", "stream-1")
	releaseSecond := resolver.DeferSessionCompaction("bot-1", "session-1", "stream-2")
	type enteredCompaction struct {
		release       chan struct{}
		done          chan struct{}
		releaseReader func()
	}
	entered := make(chan enteredCompaction, 2)
	releaseReaders := map[string]func(){"stream-1": releaseFirst, "stream-2": releaseSecond}
	for _, streamID := range []string{"stream-1", "stream-2"} {
		go func() {
			finish := resolver.enterSessionCompactionForStream("bot-1", "session-1", streamID)
			release := make(chan struct{})
			done := make(chan struct{})
			entered <- enteredCompaction{release: release, done: done, releaseReader: releaseReaders[streamID]}
			<-release
			finish()
			close(done)
		}()
	}

	var first enteredCompaction
	select {
	case first = <-entered:
	case <-time.After(time.Second):
		t.Fatal("neither sync compaction upgraded its reader")
	}
	close(first.release)
	first.releaseReader()
	var second enteredCompaction
	select {
	case second = <-entered:
	case <-time.After(time.Second):
		t.Fatal("second sync compaction deadlocked while upgrading its reader")
	}
	close(second.release)
	assertChannelReady(t, first.done, "first sync compaction did not restore its reader")
	assertChannelReady(t, second.done, "second sync compaction did not restore its reader")
	second.releaseReader()
}

func assertChannelBlocked[T any](t *testing.T, ch <-chan T, message string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatal(message)
	case <-time.After(20 * time.Millisecond):
	}
}

func assertChannelReady[T any](t *testing.T, ch <-chan T, message string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}
