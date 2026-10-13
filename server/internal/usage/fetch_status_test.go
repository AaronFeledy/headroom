package usage

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func Test_FetchFailure_classifies_only_usage_operation_failures(t *testing.T) {
	// Given
	cases := []struct {
		err  error
		kind FailureKind
	}{
		{context.DeadlineExceeded, FailureTransient}, {context.Canceled, FailureTransient},
		{io.ErrUnexpectedEOF, FailureTransient}, {&net.DNSError{Err: "synthetic"}, FailureTransient},
		{&tls.CertificateVerificationError{Err: errors.New("synthetic")}, FailureOther},
		{&net.OpError{Op: "dial", Err: x509.UnknownAuthorityError{}}, FailureOther},
		{io.EOF, FailureOther},
		{&json.SyntaxError{}, FailureOther}, {errors.New("unknown"), FailureOther},
	}
	for _, tc := range cases {
		// When
		got := TransportFailure(tc.err)
		// Then
		if got.Kind != tc.kind || !errors.Is(got, tc.err) {
			t.Fatalf("%T: %+v", tc.err, got)
		}
	}
}

func Test_FetchFailure_preserves_RetryAfter_seconds_and_date(t *testing.T) {
	// Given
	date := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	for _, value := range []string{"3600", date.Format(http.TimeFormat)} {
		response := &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{value}}}
		// When
		failure := HTTPFailure(response)
		// Then
		if failure.Kind != FailureRateLimited || failure.RetryAfter.Before(date) {
			t.Fatalf("deadline shortened: %+v", failure)
		}
	}
}

func Test_FetchStatus_serialization_preserves_frozen_error_contract(t *testing.T) {
	// Given
	errText := "synthetic outage"
	kind := FailureTransient
	epoch := CredentialEpoch("Claude", "synthetic-token")
	d := UsageData{Error: &errText, Buckets: []Bucket{{ID: "session"}}, RateLimitResetCredits: &RateLimitResetCredits{AvailableCount: 9}, FetchStatus: &FetchStatus{FetchedAt: "2026-10-09T10:00:00.123Z", FailureKind: &kind, CredentialEpoch: epoch}}
	// When
	encoded, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		FetchStatus *FetchStatus           `json:"fetch_status"`
		Buckets     []Bucket               `json:"buckets"`
		IsSuccess   bool                   `json:"is_success"`
		Credits     *RateLimitResetCredits `json:"rate_limit_reset_credits"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	// Then
	if decoded.FetchStatus == nil || *decoded.FetchStatus.FailureKind != FailureTransient || *decoded.FetchStatus.CredentialEpoch != *epoch || decoded.IsSuccess || decoded.Credits != nil || len(decoded.Buckets) != 0 {
		t.Fatalf("wire = %s", encoded)
	}
	if *epoch == *CredentialEpoch("Claude", "rotated-token") || *epoch != *CredentialEpoch("Claude", "synthetic-token") {
		t.Fatal("credential epoch unstable or rotation ignored")
	}
}
