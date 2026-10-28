package claude

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

// These tests attach only cached read-only metadata against seeded storage.
// They never call a consumer, preparation, arming, or a reset route.
func Test_BankedMetadata_deadline_omits_evidence_behind_local_mutexes(t *testing.T) {
	for _, name := range []string{"reset mutex", "bank mutex"} {
		t.Run(name, func(t *testing.T) {
			c := New(Options{})
			seedPendingJournal(t, c, journalAccount, 2)
			c.bank = bankCache{token: "fixture", status: &bankStatus{eligible: true}, attemptedAt: time.Now(), goodAt: time.Now(), accountID: journalAccount}
			mutex := &c.resetMu
			if name == "bank mutex" {
				mutex = &c.bankMu
			}
			mutex.Lock()
			release := sync.OnceFunc(mutex.Unlock)
			defer release()
			fingerprint := *accountFingerprint(journalAccount)
			data := usage.UsageData{ProviderAccountID: journalAccount, RateLimitResetCredits: &usage.RateLimitResetCredits{
				AvailableCount: 2, AccountFingerprint: &fingerprint, ReconciledRequests: func() []string { return []string{journalFixtureID} },
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			done := make(chan struct{})
			go func() {
				c.attachResetCredits(ctx, &credentials{accessToken: "fixture"}, &data)
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				release()
				select {
				case <-done:
				case <-time.After(time.Second):
				}
				t.Fatal("attachment held a mutex past its deadline")
			}
			if data.RateLimitResetCredits != nil || data.ProviderAccountID != "" {
				t.Fatal("timed-out attachment exposed reset metadata or evidence")
			}
			release()
			if !c.resetMu.TryLock() {
				t.Fatal("attachment retained reset mutex")
			}
			c.resetMu.Unlock()
			if !c.bankMu.TryLock() {
				t.Fatal("attachment retained bank mutex")
			}
			c.bankMu.Unlock()
		})
	}
}

func Test_BankedMetadata_deadline_omits_evidence_behind_journal_lock(t *testing.T) {
	// An advisory lock holder models suspended storage without a disk syscall.
	c := New(Options{})
	seedPendingJournal(t, c, journalAccount, 2)
	c.bank = bankCache{token: "fixture", status: &bankStatus{eligible: true}, attemptedAt: time.Now(), goodAt: time.Now(), accountID: journalAccount}
	lock, err := credstore.LockFile(context.Background(), c.resetStateDir+".lock")
	if err != nil {
		t.Fatal(err)
	}
	release := sync.OnceFunc(func() { _ = lock.Close() })
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	data := usage.UsageData{}
	done := make(chan struct{})
	go func() {
		c.attachResetCredits(ctx, &credentials{accessToken: "fixture"}, &data)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		release()
		t.Fatal("attachment waited past the journal-lock deadline")
	}
	if data.RateLimitResetCredits != nil || data.ProviderAccountID != "" {
		t.Fatal("journal-lock timeout exposed cached reset metadata or evidence")
	}
}
