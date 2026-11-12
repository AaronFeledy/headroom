package usage

// ResetAutomationState is the server-owned, one-shot authorization to spend one
// banked reset automatically. It is attached when a usage response is written,
// never stored in the poller cache.
type ResetAutomationState struct {
	Armed              bool                  `json:"armed"`
	ArmedAt            *string               `json:"armed_at"`
	WindowEndsAt       *string               `json:"window_ends_at"`
	Kinds              []string              `json:"kinds"`
	AccountFingerprint *string               `json:"account_fingerprint"`
	LastEvent          *ResetAutomationEvent `json:"last_event"`
}

// ResetAutomationEvent reports the latest request, completion, failure, or
// cancellation. Reason and Outcome are mutually exclusive terminal details.
type ResetAutomationEvent struct {
	At      string  `json:"at"`
	Event   string  `json:"event"`
	Outcome *string `json:"outcome"`
	Reason  *string `json:"reason"`
	Kind    *string `json:"kind"`
}
