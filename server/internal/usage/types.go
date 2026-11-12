package usage

import (
	"context"
	"encoding/json"
	"time"
)

type Provider interface {
	Name() string
	Fetch(context.Context) (UsageData, error)
}

// AccountProvider is implemented by providers whose usage row is one account of a
// provider type (for example an extra Claude account).
type AccountProvider interface {
	ProviderType() string
	Account() *Account
}

// PollPolicy bounds a provider's adaptive spacing between upstream attempts.
type PollPolicy struct {
	MinSpacing  time.Duration // never fetch sooner than this after an attempt that received an HTTP response
	MaxSpacing  time.Duration // ceiling for spacing learned from rate limits
	IdleSpacing time.Duration // ceiling for the delay while successive readings are unchanged
}

// PollPolicyProvider is implemented by providers whose poll cadence adapts to
// upstream rate limits and idle readings. The poller never fetches sooner than
// MinSpacing after an attempt that received an HTTP response.
type PollPolicyProvider interface {
	PollPolicy() PollPolicy
}

type UsageBucket struct {
	Utilization float64
	ResetsAt    *time.Time
}

func (b UsageBucket) MarshalJSON() ([]byte, error) {
	type bucketJSON struct {
		Utilization float64 `json:"utilization"`
		ResetsAt    *string `json:"resets_at"`
	}
	var resetsAt *string
	if b.ResetsAt != nil {
		formatted := b.ResetsAt.UTC().Format(time.RFC3339)
		resetsAt = &formatted
	}
	return json.Marshal(bucketJSON{Utilization: b.Utilization, ResetsAt: resetsAt})
}

type Bucket struct {
	ID          string
	Label       string
	Utilization float64
	ResetsAt    *time.Time
	StatusText  *string
	StartsAt    *time.Time
	DetailText  *string
	Measurement string `json:"measurement"`
}

func (b Bucket) MarshalJSON() ([]byte, error) {
	type bucketJSON struct {
		ID          string  `json:"id"`
		Label       string  `json:"label"`
		Utilization float64 `json:"utilization"`
		ResetsAt    *string `json:"resets_at"`
		StatusText  *string `json:"status_text"`
		StartsAt    *string `json:"starts_at"`
		DetailText  *string `json:"detail_text"`
		Measurement *string `json:"measurement"`
	}
	var resetsAt *string
	if b.ResetsAt != nil {
		formatted := b.ResetsAt.UTC().Format(time.RFC3339)
		resetsAt = &formatted
	}
	var startsAt *string
	if start := ValidPeriodStart(b.StartsAt, b.ResetsAt); start != nil {
		formatted := start.Format(time.RFC3339)
		startsAt = &formatted
	}
	var measurement *string
	if b.Measurement != "" {
		measurement = &b.Measurement
	}
	return json.Marshal(bucketJSON{StartsAt: startsAt, DetailText: b.DetailText, ID: b.ID, Label: b.Label, Utilization: b.Utilization, ResetsAt: resetsAt, StatusText: b.StatusText, Measurement: measurement})
}

type Account struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

func (a *Account) Clone() *Account {
	if a == nil {
		return nil
	}
	cp := *a
	return &cp
}

type UsageData struct {
	FetchStatus           *FetchStatus
	FetchFailure          *FetchFailure `json:"-"`
	CredentialEpoch       *string       `json:"-"`
	Auth                  Auth
	ProviderName          string
	ProviderType          string
	Account               *Account
	PrimaryLabel          string
	SecondaryLabel        string
	ShowSecondary         bool
	Subtitle              *string
	PrimaryStatusText     *string
	SecondaryStatusText   *string
	ReauthCommand         *string
	Current               UsageBucket
	Weekly                UsageBucket
	Buckets               []Bucket
	Error                 *string
	NeedsReauth           bool
	RateLimitResetCredits *RateLimitResetCredits
	UsageDetails          *UsageDetails `json:"usage_details"`
	// ResetAutomation is attached when a response is written; it never enters
	// the poller cache. Nil means the row has no automation support.
	ResetAutomation *ResetAutomationState `json:"reset_automation"`
	// SessionFromFiveHour is internal-only. It is true only when the session
	// meter came from a real five-hour window, so a weekly copy in the same slot
	// is never mistaken for a session limit.
	SessionFromFiveHour bool `json:"-"`
	// ProviderAccountID binds sensitive provider actions to the account whose
	// usage was checked. It is internal-only and must never enter API JSON.
	ProviderAccountID string `json:"-"`
}

// RateLimitResetCredits is read-only provider metadata. It is separate from
// usage buckets because a banked reset is an available action, not usage.
type RateLimitResetCredits struct {
	AvailableCount     int64   `json:"available_count"`
	AccountFingerprint *string `json:"account_fingerprint"`
	UsableNow          *bool   `json:"usable_now"`
	// Kinds is nil when a provider does not classify banked resets. Claude
	// always sets an array (possibly empty) whenever credits are known.
	Kinds []ResetCreditKind `json:"kinds"`
	// ReconciledRequests revalidates durable provider evidence at serialization
	// time, including requests proved not submitted. Cached snapshots cannot
	// retain evidence after journal damage.
	ReconciledRequests func() []string `json:"-"`
}

type Header struct {
	Current        UsageBucket
	Weekly         UsageBucket
	ShowSecondary  bool
	PrimaryLabel   string
	SecondaryLabel string
}

func DeriveHeader(buckets []Bucket) Header {
	header := Header{SecondaryLabel: "Weekly"}
	if len(buckets) == 0 {
		return header
	}

	first := buckets[0]
	header.Current = UsageBucket{Utilization: first.Utilization, ResetsAt: first.ResetsAt}
	header.PrimaryLabel = first.Label
	header.ShowSecondary = len(buckets) >= 2

	weekly := Bucket{}
	weeklyFound := false
	for _, bucket := range buckets {
		if bucket.ID == "weekly" {
			weekly = bucket
			weeklyFound = true
			break
		}
	}
	if !weeklyFound && len(buckets) >= 2 {
		weekly = buckets[1]
		weeklyFound = true
	}
	if weeklyFound {
		header.Weekly = UsageBucket{Utilization: weekly.Utilization, ResetsAt: weekly.ResetsAt}
		if len(buckets) >= 2 {
			header.SecondaryLabel = weekly.Label
		}
	}
	return header
}

func (d UsageData) WithBuckets(buckets []Bucket) UsageData {
	buckets = NormalizeBuckets(buckets)
	header := DeriveHeader(buckets)
	d.Buckets = buckets
	d.Current = header.Current
	d.Weekly = header.Weekly
	d.ShowSecondary = header.ShowSecondary
	d.PrimaryLabel = header.PrimaryLabel
	d.SecondaryLabel = header.SecondaryLabel
	return d
}

func (d UsageData) MarshalJSON() ([]byte, error) {
	type usageJSON struct {
		FetchStatus           *FetchStatus           `json:"fetch_status,omitempty"`
		ProviderName          string                 `json:"provider_name"`
		ProviderType          string                 `json:"provider_type"`
		Account               *Account               `json:"account"`
		PrimaryLabel          string                 `json:"primary_label"`
		SecondaryLabel        string                 `json:"secondary_label"`
		ShowSecondary         bool                   `json:"show_secondary"`
		Subtitle              *string                `json:"subtitle"`
		PrimaryStatusText     *string                `json:"primary_status_text"`
		SecondaryStatusText   *string                `json:"secondary_status_text"`
		ReauthCommand         *string                `json:"reauth_command"`
		Current               UsageBucket            `json:"current"`
		Weekly                UsageBucket            `json:"weekly"`
		Buckets               []Bucket               `json:"buckets"`
		Error                 *string                `json:"error"`
		NeedsReauth           bool                   `json:"needs_reauth"`
		IsSuccess             bool                   `json:"is_success"`
		RateLimitResetCredits *RateLimitResetCredits `json:"rate_limit_reset_credits"`
		Auth                  Auth                   `json:"auth"`
		UsageDetails          *UsageDetails          `json:"usage_details"`
		ResetAutomation       *ResetAutomationState  `json:"reset_automation"`
	}
	buckets := d.Buckets
	if d.Error != nil || buckets == nil {
		buckets = []Bucket{}
	}
	resetCredits := d.RateLimitResetCredits
	details := d.UsageDetails
	if d.Error != nil {
		resetCredits = nil
		details = nil
	}
	providerType := d.ProviderType
	if providerType == "" {
		providerType = d.ProviderName
	}
	auth := d.Auth
	if auth.State == "" {
		auth = NewAuth(providerType, "signed_out", nil)
		if d.Account != nil {
			auth.SignInCommand = nil
		}
	}
	return json.Marshal(usageJSON{
		FetchStatus:           d.FetchStatus,
		Auth:                  auth,
		ProviderName:          d.ProviderName,
		ProviderType:          providerType,
		Account:               d.Account,
		PrimaryLabel:          d.PrimaryLabel,
		SecondaryLabel:        d.SecondaryLabel,
		ShowSecondary:         d.ShowSecondary,
		Subtitle:              d.Subtitle,
		PrimaryStatusText:     d.PrimaryStatusText,
		SecondaryStatusText:   d.SecondaryStatusText,
		ReauthCommand:         d.ReauthCommand,
		Current:               d.Current,
		Weekly:                d.Weekly,
		Buckets:               buckets,
		Error:                 d.Error,
		NeedsReauth:           d.NeedsReauth,
		IsSuccess:             d.Error == nil,
		RateLimitResetCredits: resetCredits,
		UsageDetails:          details,
		ResetAutomation:       d.ResetAutomation,
	})
}

// ValidPeriodStart accepts a bounded provider-reported window, never an estimate.
// Keep absent/invalid timing optional so older responses remain usable.
func ValidPeriodStart(start, end *time.Time) *time.Time {
	if start == nil || end == nil || start.IsZero() || !start.Before(*end) || end.Sub(*start) > 366*24*time.Hour {
		return nil
	}
	utc := start.UTC()
	return &utc
}
