package api

import (
	"sync"
	"testing"
	"time"
)

// This exercises only the coordination helper. No handler, provider, or reset
// submission path is invoked.
func Test_ResetRejectionFinalizer_releasesSharedGateBeforeBlocking(t *testing.T) {
	var gate sync.Mutex
	gate.Lock()
	releaseGate := sync.OnceFunc(gate.Unlock)
	finalizerStarted := make(chan struct{})
	finishFinalizer := make(chan struct{})
	unblockFinalizer := sync.OnceFunc(func() { close(finishFinalizer) })
	defer unblockFinalizer()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer releaseGate() // Mirrors the handler's deferred release.
		finalizeResetRejectionAfterGate(releaseGate, func() {
			close(finalizerStarted)
			<-finishFinalizer
		})
	}()
	select {
	case <-finalizerStarted:
	case <-time.After(time.Second):
		t.Fatal("finalizer did not start")
	}
	if !gate.TryLock() {
		t.Fatal("blocked finalizer retained the shared gate")
	}
	unblockFinalizer()
	select {
	case <-done:
	case <-time.After(time.Second):
		gate.Unlock()
		t.Fatal("finalizer did not return")
	}
	if gate.TryLock() {
		gate.Unlock()
		t.Fatal("deferred release unlocked another owner's gate")
	}
	gate.Unlock()
}
