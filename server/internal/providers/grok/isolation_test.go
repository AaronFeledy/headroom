package grok

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func newIsolatedProvider(opts Options) (*Provider, error) {
	client := &http.Client{}
	if opts.HTTPClient != nil {
		*client = *opts.HTTPClient
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "GetRemainingResets") {
			return nil, errors.New("inventory tests are decoder-only")
		}
		host := req.URL.Hostname()
		_, networkTransport := transport.(*http.Transport)
		if host != "127.0.0.1" && host != "localhost" && host != "::1" && (host != "example.test" || networkTransport) {
			return nil, errors.New("test blocked nonlocal request")
		}
		if strings.HasPrefix(req.URL.Path, "/optional/") {
			return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
		return transport.RoundTrip(req)
	})
	if _, standard := transport.(*http.Transport); standard && opts.BillingURL == "" {
		client.Transport = transport
	}
	opts.HTTPClient = client
	if opts.BillingURL == "" {
		opts.BillingURL = opts.TokenURL
	}
	if base, err := url.Parse(opts.BillingURL); err == nil && base.Host != "" {
		for i, dest := range []*string{&opts.PlainBillingURL, &opts.AutoTopUpURL, &opts.UserURL, &opts.ResetInventoryURL} {
			if *dest == "" {
				ref := &url.URL{Path: []string{"/optional/plain-billing", "/optional/auto-topup-rule", "/optional/user", "/GetRemainingResets"}[i]}
				*dest = base.ResolveReference(ref).String()
			}
		}
	}
	return NewProvider(opts)
}
