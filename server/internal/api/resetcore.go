package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/resetauto"
)

// ResetCore owns the single cross-provider reset gate. Every handler instance
// in one process must share the same core, so a manual reset and an automatic
// reset can never spend at the same time.
type ResetCore struct {
	codex  ProviderResetter
	claude ProviderResetter
	poller ProviderPoller
	names  []string

	mu                   sync.Mutex
	refreshMu            sync.Mutex
	codexRefreshPending  bool
	claudeRefreshPending bool
}

type ResetCoreOptions struct {
	Codex         ProviderResetter
	Claude        ProviderResetter
	Poller        ProviderPoller
	ProviderNames []string
}

func NewResetCore(opts ResetCoreOptions) *ResetCore {
	return &ResetCore{codex: opts.Codex, claude: opts.Claude, poller: opts.Poller, names: opts.ProviderNames}
}

// ResetRequest is a validated, confirmed reset. Callers never retry it.
type ResetRequest struct {
	RequestID          string
	AccountFingerprint string
	Kind               string
	// Confirm, when set, must accept the reading this attempt spends on. The
	// automatic path uses it to hold the spend to its own firing rule.
	Confirm func(poller.Entry) bool
}

// ResetResult carries the handler-facing outcome. Status is zero on success.
type ResetResult struct {
	Outcome   string
	Ambiguous bool
	Status    int
	Message   string
}

// Execute runs one reset attempt. It never contacts a provider before the gate,
// and it never repeats the consume call.
func (c *ResetCore) Execute(ctx context.Context, provider string, request ResetRequest) ResetResult {
	resetter, label, eligible := c.resetter(provider)
	if resetter == nil || c.poller == nil || !c.hasProvider(provider) {
		return ResetResult{Status: http.StatusNotFound, Message: "provider not found"}
	}
	if ctx.Err() != nil {
		finalizeResetRejection(provider, resetter, request)
		return ResetResult{Status: http.StatusRequestTimeout, Message: "reset request timed out"}
	}
	if !c.mu.TryLock() {
		finalizeResetRejection(provider, resetter, request)
		return ResetResult{Status: http.StatusConflict, Message: "another reset request is in progress"}
	}
	releaseResetGate := sync.OnceFunc(c.mu.Unlock)
	defer releaseResetGate()
	known, blocked := resetter.ResetAttemptStatus(request.RequestID)
	if blocked {
		finalizeResetRejectionAfterGate(releaseResetGate, func() { finalizeResetRejection(provider, resetter, request) })
		return ResetResult{Status: http.StatusConflict, Message: "previous reset awaiting usage confirmation; no retry permitted"}
	}
	expectedAccountID := ""
	if !known {
		if provider == "claude" && request.Kind != "" {
			kind := request.Kind
			eligible = func(entry poller.Entry) bool { return eligibleForClaudeResetKind(entry, kind) }
		}
		entry, ok, err := c.poller.PollProvider(ctx, provider)
		if err != nil || !ok || !eligible(entry) || entry.Data.RateLimitResetCredits.AccountFingerprint == nil || *entry.Data.RateLimitResetCredits.AccountFingerprint != request.AccountFingerprint {
			finalizeResetRejectionAfterGate(releaseResetGate, func() { finalizeResetRejection(provider, resetter, request) })
			return ResetResult{Status: http.StatusConflict, Message: "fresh eligible " + label + " usage is required"}
		}
		if provider == "claude" && request.Kind == "" && claudeResetKindAmbiguous(entry) {
			finalizeResetRejectionAfterGate(releaseResetGate, func() { finalizeResetRejection(provider, resetter, request) })
			return ResetResult{Status: http.StatusConflict, Message: multiKindResetMessage}
		}
		if request.Confirm != nil && !request.Confirm(entry) {
			finalizeResetRejectionAfterGate(releaseResetGate, func() { finalizeResetRejection(provider, resetter, request) })
			return ResetResult{Status: http.StatusConflict, Message: "fresh eligible " + label + " usage is required"}
		}
		expectedAccountID = entry.Data.ProviderAccountID
	}
	var outcome string
	var ambiguous bool
	var err error
	if request.Kind != "" {
		kindResetter, kindOK := resetter.(ProviderKindResetter)
		if !kindOK {
			finalizeResetRejectionAfterGate(releaseResetGate, func() { finalizeResetRejection(provider, resetter, request) })
			return ResetResult{Status: http.StatusConflict, Message: "fresh eligible " + label + " usage is required"}
		}
		outcome, ambiguous, err = kindResetter.ConsumeResetCreditKind(ctx, request.RequestID, expectedAccountID, request.AccountFingerprint, request.Kind)
	} else {
		outcome, ambiguous, err = resetter.ConsumeResetCredit(ctx, request.RequestID, expectedAccountID, request.AccountFingerprint)
	}
	// Reconcile even an uncertain response with a delayed read-only fetch. Never
	// repeat ConsumeResetCredit automatically or exercise this flow in tests.
	c.scheduleUsageRefresh(provider)
	if err != nil {
		if ambiguous {
			return ResetResult{Ambiguous: true, Status: http.StatusBadGateway, Message: "reset outcome unknown; request will not be retried"}
		}
		finalizeResetRejectionAfterGate(releaseResetGate, func() { finalizeResetRejection(provider, resetter, request) })
		return ResetResult{Status: http.StatusBadGateway, Message: "reset request unavailable"}
	}
	return ResetResult{Outcome: outcome}
}

func (c *ResetCore) resetter(provider string) (ProviderResetter, string, func(poller.Entry) bool) {
	if provider == "claude" {
		return c.claude, "Claude", eligibleForClaudeReset
	}
	return c.codex, "ChatGPT", eligibleForCodexReset
}

func (c *ResetCore) hasProvider(name string) bool {
	for _, providerName := range c.names {
		if strings.EqualFold(providerName, name) {
			return true
		}
	}
	return false
}

func (c *ResetCore) scheduleUsageRefresh(provider string) {
	c.refreshMu.Lock()
	pending := &c.codexRefreshPending
	if provider == "claude" {
		pending = &c.claudeRefreshPending
	}
	if *pending {
		c.refreshMu.Unlock()
		return
	}
	*pending = true
	c.refreshMu.Unlock()
	go func() {
		defer func() {
			c.refreshMu.Lock()
			*pending = false
			c.refreshMu.Unlock()
		}()
		// Give the provider time to apply the reset, then update the ordinary usage
		// cache once. This is only a provider GET, never another redemption.
		// It outlives the response but has its own bounded fetch deadline.
		time.Sleep(3 * time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _, _ = c.poller.PollProvider(ctx, provider)
	}()
}

func (c *ResetCore) AutomationExecutor() resetauto.Executor {
	return func(ctx context.Context, request resetauto.ExecutorRequest) resetauto.ExecutorResult {
		result := c.Execute(ctx, request.Provider, ResetRequest{
			RequestID:          request.RequestID,
			AccountFingerprint: request.AccountFingerprint,
			Kind:               request.Kind,
			Confirm:            request.Confirm,
		})
		return resetauto.ExecutorResult{
			Outcome:     result.Outcome,
			Ambiguous:   result.Ambiguous,
			Unavailable: result.Status == http.StatusNotFound,
		}
	}
}
