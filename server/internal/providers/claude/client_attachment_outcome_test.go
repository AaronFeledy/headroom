package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

type closeSignalBody struct {
	io.Reader
	onClose func()
	once    sync.Once
}

func (b *closeSignalBody) Close() error {
	b.once.Do(b.onClose)
	return nil
}

type usageOnlyTransport struct {
	calls  atomic.Int32
	closed chan int32
}

func (t *usageOnlyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet || req.URL.Path != "/usage" || req.URL.RawQuery != "" {
		return nil, fmt.Errorf("only the plain read-only usage GET is allowed: %s %s", req.Method, req.URL)
	}
	call := t.calls.Add(1)
	utilization := 96
	if call == 2 {
		utilization = 12
	} else if call == 3 {
		utilization = 13
	} else if call == 4 {
		utilization = 97
	}
	body := fmt.Sprintf(`{"five_hour":{"utilization":%d},"seven_day":{"utilization":95}}`, utilization)
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: &closeSignalBody{
		Reader: strings.NewReader(body), onClose: func() { t.closed <- call },
	}, Request: req}, nil
}

type recordingClaudeProvider struct {
	client   *Client
	lastData usage.UsageData
	lastErr  error
}

func (p *recordingClaudeProvider) Name() string { return p.client.Name() }

func (p *recordingClaudeProvider) Fetch(ctx context.Context) (usage.UsageData, error) {
	p.lastData, p.lastErr = p.client.Fetch(ctx)
	return p.lastData, p.lastErr
}

func Test_BankedMetadata_attachment_context_failure_preserves_poller_last_good(t *testing.T) {
	// The transport permits only a plain usage GET. Bank metadata and the
	// journal are seeded; no consumer, arming, preparation, or reset route runs.
	transport := &usageOnlyTransport{closed: make(chan int32, 4)}
	credentialsPath := writeClaudeCredentials(t, credentialFixture{Access: "fixture", Refresh: "fixture-refresh", ExpiresAt: futureMillis(t)})
	c := New(Options{CredentialsPath: credentialsPath, UsageURL: "http://fixture.invalid/usage", HTTPClient: &http.Client{Transport: transport}})
	seedJournal(t, c, resetJournal{Version: 1, Attempts: map[string]resetAttempt{}})
	status := parseBankStatus([]byte(`{"cedar_ember":{"eligible":true,"next_grant_id":"fixture","grants":[{"id":"fixture","resets_total":2,"resets_left":2,"paused":false,"usable_now":true}]}}`))
	if status == nil {
		t.Fatal("invalid seeded bank status")
	}
	c.bank = bankCache{token: "fixture", status: status, attemptedAt: time.Now(), goodAt: time.Now(), accountID: journalAccount}
	provider := &recordingClaudeProvider{client: c}
	p := poller.New(poller.Options{})
	if err := p.Register(provider, true); err != nil {
		t.Fatal(err)
	}
	first, ok, err := p.PollProvider(context.Background(), "Claude")
	if !ok || err != nil || provider.lastErr != nil || first.Data.Error != nil || first.Data.RateLimitResetCredits == nil || first.Data.RateLimitResetCredits.AvailableCount != 2 {
		t.Fatalf("seeded first poll did not establish last good: ok=%t err=%v fetch=%v", ok, err, provider.lastErr)
	}
	if got := <-transport.closed; got != 1 {
		t.Fatalf("first usage GET = %d, want 1", got)
	}
	c.resetMu.Lock()
	release := sync.OnceFunc(c.resetMu.Unlock)
	defer release()
	type result struct {
		entry poller.Entry
		ok    bool
		err   error
	}
	poll := func(ctx context.Context, wantGET int32, afterGET func()) result {
		t.Helper()
		done := make(chan result, 1)
		go func() {
			entry, ok, err := p.PollProvider(ctx, "Claude")
			done <- result{entry, ok, err}
		}()
		select {
		case got := <-transport.closed:
			if got != wantGET {
				release()
				t.Fatalf("plain usage GET = %d, want %d", got, wantGET)
			}
		case <-time.After(3 * time.Second):
			release()
			t.Fatal("plain usage GET did not finish")
		}
		if afterGET != nil {
			afterGET()
		}
		select {
		case got := <-done:
			return got
		case <-time.After(3 * time.Second):
			release()
			t.Fatal("poll did not stop after attachment context ended")
		}
		return result{}
	}
	assertFailedPoll := func(got result, wantErr error) {
		t.Helper()
		if !got.ok || got.err != nil || !errors.Is(provider.lastErr, wantErr) {
			t.Fatalf("attachment failure not propagated: ok=%t poll=%v fetch=%v", got.ok, got.err, provider.lastErr)
		}
		if provider.lastData.RateLimitResetCredits != nil || provider.lastData.ProviderAccountID != "" {
			t.Fatal("failed fresh attachment retained reset metadata or binding")
		}
		if got.entry.Data.Error == nil || got.entry.Data.Current.Utilization != 96 || got.entry.Data.ProviderAccountID != journalAccount ||
			got.entry.Data.RateLimitResetCredits == nil || got.entry.Data.RateLimitResetCredits.AvailableCount != 2 {
			t.Fatal("poller replaced last good instead of overlaying a failed fetch")
		}
		encoded, err := json.Marshal(got.entry.Data)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			IsSuccess bool              `json:"is_success"`
			Error     *string           `json:"error"`
			Credits   json.RawMessage   `json:"rate_limit_reset_credits"`
			Buckets   []json.RawMessage `json:"buckets"`
		}
		if err := json.Unmarshal(encoded, &wire); err != nil {
			t.Fatal(err)
		}
		if wire.IsSuccess || wire.Error == nil || string(wire.Credits) != "null" || len(wire.Buckets) != 0 {
			t.Fatalf("failed wire reading exposed spendable evidence: %s", encoded)
		}
	}
	deadlineCtx, deadlineCancel := context.WithTimeout(context.Background(), time.Second)
	defer deadlineCancel()
	assertFailedPoll(poll(deadlineCtx, 2, nil), context.DeadlineExceeded)
	cancelCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	assertFailedPoll(poll(cancelCtx, 3, cancel), context.Canceled)
	release()
	recovered, ok, err := p.PollProvider(context.Background(), "Claude")
	if !ok || err != nil || provider.lastErr != nil || recovered.Data.Error != nil || recovered.Data.Current.Utilization != 97 ||
		recovered.Data.RateLimitResetCredits == nil || recovered.Data.RateLimitResetCredits.AvailableCount != 2 {
		t.Fatalf("healthy poll did not recover after attachment failure: ok=%t poll=%v fetch=%v", ok, err, provider.lastErr)
	}
	if got := <-transport.closed; got != 4 {
		t.Fatalf("recovery usage GET = %d, want 4", got)
	}
}
