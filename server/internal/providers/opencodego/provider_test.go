package opencodego

import (
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Provider_Fetch_decodes_all_windows_and_reset_forms(t *testing.T) {
	// Given
	server := newUsageServer(t, 0, `{"usage":{
		"rolling":{"percent":100,"resetInSec":15900},
		"weekly":{"percent":80,"resetsAt":"2026-08-12T02:00:00.000Z"},
		"monthly":{"percent":90,"resetAt":"2026-09-01T00:00:00Z"}}}`)
	home := t.TempDir()
	writeLegacyAuth(t, home, `{"opencode-go":{"type":"api","key":"fixture-token"}}`)
	provider := newProvider(t, testEnvironment(t, home, nil), server.url())

	// When
	data := fetch(t, provider)

	// Then
	if data.Error != nil {
		t.Fatalf("UsageData.Error = %q, want nil", *data.Error)
	}
	if data.ProviderName != "OpenCodeGo" || data.ProviderType != "" {
		t.Fatalf("provider name/type = %q/%q", data.ProviderName, data.ProviderType)
	}
	if len(data.Buckets) != 3 {
		t.Fatalf("buckets = %#v, want three windows", data.Buckets)
	}
	session, ok := bucketByID(data, usage.BucketSession)
	if !ok || session.Label != "5-Hour" || session.Utilization != 100 || session.Measurement != usage.MeasurementPercent || session.StartsAt != nil {
		t.Fatalf("session bucket = %#v", session)
	}
	if session.ResetsAt == nil || !session.ResetsAt.Equal(fixedNow.Add(15900*time.Second)) {
		t.Fatalf("session reset = %v, want now+15900s", session.ResetsAt)
	}
	weekly, ok := bucketByID(data, usage.BucketWeekly)
	if !ok || weekly.Label != "Weekly" || weekly.Utilization != 80 {
		t.Fatalf("weekly bucket = %#v", weekly)
	}
	if weekly.ResetsAt == nil || weekly.ResetsAt.Format(time.RFC3339Nano) != "2026-08-12T02:00:00Z" {
		t.Fatalf("weekly reset = %v", weekly.ResetsAt)
	}
	monthly, ok := bucketByID(data, usage.BucketMonthly)
	if !ok || monthly.Label != "Monthly" || monthly.Utilization != 90 {
		t.Fatalf("monthly bucket = %#v", monthly)
	}
	if monthly.ResetsAt == nil || monthly.ResetsAt.Format(time.RFC3339Nano) != "2026-09-01T00:00:00Z" {
		t.Fatalf("monthly reset = %v", monthly.ResetsAt)
	}
	if data.Current.Utilization != 100 || data.PrimaryLabel != "5-Hour" || !data.ShowSecondary {
		t.Fatalf("header = %#v", data)
	}
	if data.Auth.State != "signed_in" || data.Auth.Source == nil || data.Auth.Source.Kind != "cli" || data.Auth.SignInCommand != nil {
		t.Fatalf("auth = %#v", data.Auth)
	}
	auths, paths, requests := server.recorded()
	if requests != 1 || auths[0] != "Bearer fixture-token" || paths[0] != "/zen/go/v1/usage" {
		t.Fatalf("requests = %d auth=%v paths=%v", requests, auths, paths)
	}
	accepts, agents := server.headers()
	if accepts[0] != "application/json" || agents[0] != "ClaudeUsageWidget" {
		t.Fatalf("accept = %q user agent = %q", accepts[0], agents[0])
	}
}

func Test_Provider_Fetch_optional_windows(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		want        []string
		utilization []float64
	}{
		{name: "rolling only", body: `{"usage":{"rolling":{"percent":0}}}`, want: []string{usage.BucketSession}, utilization: []float64{0}},
		{name: "no monthly reset", body: `{"usage":{"rolling":{"percent":12},"monthly":{"percent":100}}}`, want: []string{usage.BucketSession, usage.BucketMonthly}, utilization: []float64{12, 100}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			server := newUsageServer(t, 0, test.body)
			home := t.TempDir()
			writeLegacyAuth(t, home, `{"opencode-go":{"type":"api","key":"fixture-token"}}`)
			provider := newProvider(t, testEnvironment(t, home, nil), server.url())

			// When
			data := fetch(t, provider)

			// Then
			if data.Error != nil {
				t.Fatalf("UsageData.Error = %q, want nil", *data.Error)
			}
			if len(data.Buckets) != len(test.want) {
				t.Fatalf("buckets = %#v, want %v", data.Buckets, test.want)
			}
			for index, id := range test.want {
				if data.Buckets[index].ID != id || data.Buckets[index].Utilization != test.utilization[index] {
					t.Fatalf("bucket %d = %#v, want %q at %v", index, data.Buckets[index], id, test.utilization[index])
				}
			}
			for _, bucket := range data.Buckets {
				if bucket.StartsAt != nil || bucket.ResetsAt != nil || bucket.Measurement != usage.MeasurementPercent {
					t.Fatalf("bucket = %#v", bucket)
				}
			}
		})
	}
}

func Test_Provider_Fetch_missing_rolling_is_a_parse_failure(t *testing.T) {
	// Given
	server := newUsageServer(t, 0, `{"usage":{"weekly":{"percent":50}}}`)
	home := t.TempDir()
	writeLegacyAuth(t, home, `{"opencode-go":{"type":"api","key":"fixture-token"}}`)
	provider := newProvider(t, testEnvironment(t, home, nil), server.url())

	// When
	data := fetch(t, provider)

	// Then
	if errorText(data) != "OpenCode Go usage response changed." {
		t.Fatalf("error = %q", errorText(data))
	}
	if data.FetchFailure == nil || data.FetchFailure.Kind != usage.FailureOther {
		t.Fatalf("failure = %#v", data.FetchFailure)
	}
	if data.NeedsReauth || data.Auth.State != "signed_in" {
		t.Fatalf("auth = %#v", data.Auth)
	}
	if len(data.Buckets) != 0 {
		t.Fatalf("buckets = %#v, want none", data.Buckets)
	}
}

func Test_Provider_Fetch_clamps_percent_without_scaling_fractions(t *testing.T) {
	tests := []struct {
		name     string
		percent  string
		expected float64
	}{
		{name: "above range", percent: "150", expected: 100},
		{name: "below range", percent: "-5", expected: 0},
		{name: "fraction is a percent", percent: "0.5", expected: 0.5},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":`+test.percent+`}}}`)
			home := t.TempDir()
			writeLegacyAuth(t, home, `{"opencode-go":{"type":"api","key":"fixture-token"}}`)
			provider := newProvider(t, testEnvironment(t, home, nil), server.url())

			// When
			data := fetch(t, provider)

			// Then
			if data.Error != nil {
				t.Fatalf("UsageData.Error = %q", *data.Error)
			}
			if len(data.Buckets) != 1 || math.Abs(data.Buckets[0].Utilization-test.expected) > 1e-9 {
				t.Fatalf("buckets = %#v, want %v", data.Buckets, test.expected)
			}
			if data.Buckets[0].ResetsAt != nil {
				t.Fatalf("reset = %v, want nil without upstream metadata", data.Buckets[0].ResetsAt)
			}
		})
	}
}

func Test_Provider_Fetch_reports_rejected_credentials_as_expired(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(httpStatusName(status), func(t *testing.T) {
			// Given
			server := newUsageServer(t, status, "")
			home := t.TempDir()
			writeLegacyAuth(t, home, `{"opencode-go":{"type":"api","key":"fixture-token"}}`)
			provider := newProvider(t, testEnvironment(t, home, nil), server.url())

			// When
			data := fetch(t, provider)

			// Then
			if !data.NeedsReauth || data.Auth.State != "expired" {
				t.Fatalf("auth = %#v", data.Auth)
			}
			if errorText(data) != "OpenCode Go auth expired. Run `opencode auth login` again." {
				t.Fatalf("error = %q", errorText(data))
			}
			if data.FetchFailure == nil || data.FetchFailure.Kind != usage.FailureAuth {
				t.Fatalf("failure = %#v", data.FetchFailure)
			}
			if data.Auth.SignInCommand == nil || *data.Auth.SignInCommand != "opencode auth login" {
				t.Fatalf("sign-in command = %v", data.Auth.SignInCommand)
			}
		})
	}
}

func Test_Provider_Fetch_defaults_to_upstream_usage_URL(t *testing.T) {
	// Given
	provider := NewProvider(Options{Environment: testEnvironment(t, t.TempDir(), nil)})

	// When and Then
	if provider.usageURL != "https://opencode.ai/zen/go/v1/usage" {
		t.Fatalf("usage URL = %q", provider.usageURL)
	}
}

func Test_Provider_Fetch_never_follows_a_redirect_to_another_origin(t *testing.T) {
	// Given a usage endpoint that redirects to a different origin
	other := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":1}}}`)
	redirecting := httptest.NewServer(http.RedirectHandler(other.url(), http.StatusFound))
	t.Cleanup(redirecting.Close)
	home := t.TempDir()
	writeLegacyAuth(t, home, `{"opencode-go":{"type":"api","key":"fixture-token"}}`)
	provider := newProvider(t, testEnvironment(t, home, nil), redirecting.URL+"/zen/go/v1/usage")

	// When
	data := fetch(t, provider)

	// Then the request, and with it the bearer token, never reaches that origin
	if _, _, requests := other.recorded(); requests != 0 {
		t.Fatalf("redirect target received %d requests", requests)
	}
	if data.Error == nil || len(data.Buckets) != 0 {
		t.Fatalf("error = %v buckets = %#v", data.Error, data.Buckets)
	}
}

func httpStatusName(status int) string {
	switch status {
	case 401:
		return "unauthorized"
	case 403:
		return "forbidden"
	default:
		return "other"
	}
}

func Test_Provider_Fetch_prefers_resetInSec_over_absolute_timestamps(t *testing.T) {
	// Given
	server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":1,"resetInSec":60,"resetsAt":"2030-01-01T00:00:00Z","resetAt":"2031-01-01T00:00:00Z"}}}`)
	home := t.TempDir()
	writeLegacyAuth(t, home, `{"opencode-go":{"type":"api","key":"fixture-token"}}`)
	provider := newProvider(t, testEnvironment(t, home, nil), server.url())

	// When
	data := fetch(t, provider)

	// Then
	if len(data.Buckets) != 1 || data.Buckets[0].ResetsAt == nil || !data.Buckets[0].ResetsAt.Equal(fixedNow.Add(time.Minute)) {
		t.Fatalf("reset = %v, want now+60s", data.Buckets[0].ResetsAt)
	}
}

func Test_Provider_Fetch_credential_epoch_follows_the_credential(t *testing.T) {
	// Given a signed-in provider whose upstream fails on the second poll
	server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":1}}}`)
	failing := newUsageServer(t, http.StatusInternalServerError, "")
	home := t.TempDir()
	authPath := writeLegacyAuth(t, home, `{"opencode-go":{"type":"api","key":"first-token"}}`)
	environment := testEnvironment(t, home, nil)
	provider := newProvider(t, environment, server.url())
	failingProvider := newProvider(t, environment, failing.url())

	// When the same key is polled successfully and unsuccessfully, then replaced
	first := fetch(t, provider)
	failed := fetch(t, failingProvider)
	writeLegacyAuth(t, home, `{"opencode-go":{"type":"api","key":"second-token"}}`)
	setModTime(t, authPath, millisAt(time.Hour))
	rotated := fetch(t, provider)

	// Then a failure keeps the epoch and a new key changes it
	if first.CredentialEpoch == nil || failed.CredentialEpoch == nil || rotated.CredentialEpoch == nil {
		t.Fatalf("epochs = %v / %v / %v, want all set", first.CredentialEpoch, failed.CredentialEpoch, rotated.CredentialEpoch)
	}
	if failed.Error == nil || *failed.CredentialEpoch != *first.CredentialEpoch {
		t.Fatalf("failed poll error = %v, epoch changed", failed.Error)
	}
	if *rotated.CredentialEpoch == *first.CredentialEpoch {
		t.Fatal("rotated key kept the previous epoch")
	}
}
