package usage

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

type FailureKind string

const (
	FailureTransient   FailureKind = "transient"
	FailureRateLimited FailureKind = "rate_limited"
	FailureAuth        FailureKind = "auth"
	FailureOther       FailureKind = "other"
)

type FetchStatus struct {
	FetchedAt       string            `json:"fetched_at"`
	FailureKind     *FailureKind      `json:"failure_kind"`
	CredentialEpoch *string           `json:"credential_epoch"`
	Diagnostics     *FetchDiagnostics `json:"diagnostics,omitempty"`
}

// FetchOperation labels are source-defined categories, never URLs or provider text.
type FetchOperation string

const (
	OperationUsage       FetchOperation = "usage"
	OperationResetStatus FetchOperation = "reset_status"
	OperationProfile     FetchOperation = "profile"
)

// FetchDiagnostics is a bounded, account-free summary of the last provider attempt.
type FetchDiagnostics struct {
	Operation      *string `json:"operation"`
	HTTPStatus     *int    `json:"http_status"`
	RetryAfter     *string `json:"retry_after"`
	NextAttemptAt  string  `json:"next_attempt_at"`
	CooldownUntil  *string `json:"cooldown_until"`
	CooldownSource *string `json:"cooldown_source"`
}

// FetchFailure is operation-local, internal scheduling metadata, never wire data.
type FetchFailure struct {
	Kind              FailureKind
	RetryAfter        time.Time
	RateLimitFallback bool
	// MinCooldown is an optional provider-declared floor used when the
	// upstream 429 carries no usable Retry-After. Diagnostics keep reporting
	// RetryAfter as the bare upstream header.
	MinCooldown time.Duration
	Operation   FetchOperation
	HTTPStatus  int
	err         error
}

func (f *FetchFailure) Error() string { return "usage request failed" }
func (f *FetchFailure) Unwrap() error { return f.err }

func IsTimeout(err error) bool {
	var timeout net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout())
}

// TransportFailure applies only to read-only usage operations, not OAuth refresh.
func TransportFailure(err error) *FetchFailure {
	kind := FailureOther
	var verify *tls.CertificateVerificationError
	var network net.Error
	var operation *net.OpError
	var dns *net.DNSError
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var certificate x509.CertificateInvalidError
	var roots x509.SystemRootsError
	switch {
	case errors.As(err, &verify), errors.As(err, &authority), errors.As(err, &hostname), errors.As(err, &certificate), errors.As(err, &roots):
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled), errors.Is(err, io.ErrUnexpectedEOF):
		kind = FailureTransient
	case errors.As(err, &network) && network.Timeout(), errors.As(err, &operation), errors.As(err, &dns):
		kind = FailureTransient
	}
	return &FetchFailure{Kind: kind, err: err}
}

func HTTPFailure(response *http.Response) *FetchFailure {
	f := &FetchFailure{Kind: FailureOther, Operation: OperationUsage, HTTPStatus: response.StatusCode}
	switch {
	case response.StatusCode == http.StatusTooManyRequests:
		f.Kind = FailureRateLimited
		value := response.Header.Get("Retry-After")
		if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds >= 0 {
			f.RetryAfter = time.Now().Add(time.Duration(seconds) * time.Second)
		} else if date, err := http.ParseTime(value); err == nil {
			f.RetryAfter = date
		}
		f.RateLimitFallback = f.RetryAfter.IsZero()
	case response.StatusCode == http.StatusRequestTimeout, response.StatusCode >= 500:
		f.Kind = FailureTransient
	case response.StatusCode == http.StatusUnauthorized, response.StatusCode == http.StatusForbidden:
		f.Kind = FailureAuth
	}
	return f
}

var epochSecret = rand.Text()

// CredentialEpoch exposes only a process-secret HMAC, not a credential digest.
func CredentialEpoch(parts ...string) *string {
	mac := hmac.New(sha256.New, []byte(epochSecret))
	for _, part := range parts {
		mac.Write([]byte(strconv.Itoa(len(part)) + ":" + part))
	}
	value := hex.EncodeToString(mac.Sum(nil))
	return &value
}
