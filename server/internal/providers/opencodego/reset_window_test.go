package opencodego

import (
	"testing"
	"time"
)

func Test_Provider_Fetch_keeps_a_relative_reset_for_the_same_window(t *testing.T) {
	// Given a window reported as seconds until reset on every poll
	home := t.TempDir()
	authPath := writeLegacyAuth(t, home, `{"opencode-go":{"type":"api","key":"first-token"}}`)
	provider := newProvider(t, testEnvironment(t, home, nil), "")
	poll := func(at time.Duration, resetInSec string) time.Time {
		t.Helper()
		provider.now = func() time.Time { return fixedNow.Add(at) }
		provider.usageURL = newUsageServer(t, 0, `{"usage":{"rolling":{"percent":1,"resetInSec":`+resetInSec+`}}}`).url()
		data := fetch(t, provider)
		if len(data.Buckets) != 1 || data.Buckets[0].ResetsAt == nil {
			t.Fatalf("buckets = %+v, want one with a reset", data.Buckets)
		}
		return *data.Buckets[0].ResetsAt
	}

	// When later polls drift by part of a second, then the window rolls over
	first := poll(0, "3600")
	drifted := poll(1500*time.Millisecond, "3598")
	rolled := poll(2*time.Hour, "18000")
	writeLegacyAuth(t, home, `{"opencode-go":{"type":"api","key":"second-token"}}`)
	setModTime(t, authPath, millisAt(3*time.Hour))
	signedIn := poll(2*time.Hour+time.Second, "18000")

	// Then the same window keeps its first reset, and a new window or key does not
	if !first.Equal(fixedNow.Add(time.Hour)) || !drifted.Equal(first) {
		t.Fatalf("resets = %v then %v, want both %v", first, drifted, fixedNow.Add(time.Hour))
	}
	if !rolled.Equal(fixedNow.Add(7 * time.Hour)) {
		t.Fatalf("rolled reset = %v, want %v", rolled, fixedNow.Add(7*time.Hour))
	}
	if !signedIn.Equal(fixedNow.Add(7*time.Hour + time.Second)) {
		t.Fatalf("reset after a new key = %v, want %v", signedIn, fixedNow.Add(7*time.Hour+time.Second))
	}
}
