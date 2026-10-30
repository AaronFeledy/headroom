package poller

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Recovery_nonfuture_RetryAfter_uses_bounded_backoff(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
	}{
		{"missing", ""},
		{"zero", "0"},
		{"negative", "-1"},
		{"malformed", "invalid"},
		{"expired date", "Thu, 01 Jan 1970 00:00:00 GMT"},
		{"expires before scheduling", "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Parse a real header, then advance past its deadline without sleeping.
			failure := usage.HTTPFailure(&http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     http.Header{"Retry-After": []string{tc.header}},
			})
			provider := &sequenceProvider{name: "Synthetic", responses: []providerResult{{data: usage.UsageData{
				Error: strPtr("limited"), FetchFailure: failure,
			}}}}
			p, clock, _ := recoveryPoller(t, provider)
			clock.next(time.Now().Add(2 * time.Second).Sub(clock.Now()))
			p.dispatchDue(p.enabledStates())
			settle(p)

			for index, seconds := range []int{60, 120, 240, 300, 300} {
				clock.next(time.Duration(seconds-1) * time.Second)
				// Manual and synchronous reads must honor the same cooldown.
				if result, err := p.RequestRefresh(); !errors.Is(err, ErrRefreshRateLimited) || result.RetryAfterSeconds != 1 {
					t.Fatalf("protected refresh = %+v, %v", result, err)
				}
				p.PollProvider(context.Background(), "Synthetic")
				p.dispatchDue(p.enabledStates())
				settle(p)
				if got := provider.calls.Load(); got != int32(index+1) {
					t.Fatalf("early dispatch: calls = %d, want %d", got, index+1)
				}
				clock.next(time.Second)
				p.dispatchDue(p.enabledStates())
				settle(p)
				if got := provider.calls.Load(); got != int32(index+2) {
					t.Fatalf("due dispatch: calls = %d, want %d", got, index+2)
				}
			}
		})
	}
}
