package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func proofServer(t *testing.T, valid bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var sent atomic.Int32
	server := httptest.NewUnstartedServer(nil)
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sent.Add(1)
		}
		if r.URL.Path == "/api/v1/tls/proof" {
			if r.Header.Get("Authorization") != "" {
				t.Error("token sent in proof")
			}
			hash := sha256.Sum256(server.TLS.Certificates[0].Certificate[0])
			fingerprint := hex.EncodeToString(hash[:])
			proof := hex.EncodeToString(proofMessage("fixture-token", r.URL.Query().Get("nonce"), fingerprint))
			if !valid {
				proof = strings.Repeat("0", 64)
			}
			if err := json.NewEncoder(w).Encode(struct {
				CertificateSHA256 string `json:"certificate_sha256"`
				Proof             string `json:"proof"`
			}{fingerprint, proof}); err != nil {
				t.Error(err)
			}
			return
		}
		if r.TLS == nil {
			t.Error("authenticated request not upgraded")
		}
		if _, err := io.WriteString(w, "[]"); err != nil {
			t.Error(err)
		}
	})
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, &sent
}

func Test_CLI_TLS_proof_pins_before_sending_token(t *testing.T) {
	// Given
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "valid", false: "mismatch"}[valid], func(t *testing.T) {
			server, sent := proofServer(t, valid)
			options := Options{Env: []string{"HEADROOM_AUTH_TOKEN=fixture-token"}}.defaults()
			// When
			body, err := fetchHTTP(context.Background(), options, server.URL)
			// Then
			if valid {
				if err != nil || string(body) != "[]" || sent.Load() != 1 {
					t.Fatalf("body=%s sent=%d err=%v", body, sent.Load(), err)
				}
			} else {
				if err == nil || sent.Load() != 0 {
					t.Fatalf("sent=%d err=%v", sent.Load(), err)
				}
			}
		})
	}
}

func Test_CLI_HTTP_upgrades_on_same_port(t *testing.T) {
	// Given
	server, sent := proofServer(t, true)
	options := Options{Env: []string{"HEADROOM_AUTH_TOKEN=fixture-token"}}.defaults()
	// When
	body, err := fetchHTTP(context.Background(), options, strings.Replace(server.URL, "https://", "http://", 1))
	// Then
	if err != nil || string(body) != "[]" || sent.Load() != 1 {
		t.Fatalf("body=%s sent=%d err=%v", body, sent.Load(), err)
	}
}

func Test_CLI_HTTP_falls_back_silently_for_old_server(t *testing.T) {
	// Given
	var sent atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer fixture-token" {
			sent.Add(1)
		}
		if _, err := io.WriteString(w, "[]"); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	options := Options{Env: []string{"HEADROOM_AUTH_TOKEN=fixture-token"}}.defaults()
	// When
	body, err := fetchHTTP(context.Background(), options, server.URL)
	// Then
	if err != nil || string(body) != "[]" || sent.Load() != 1 {
		t.Fatalf("body=%s sent=%d err=%v", body, sent.Load(), err)
	}
}

func Test_CLI_render_includes_auth_sign_in_hints(t *testing.T) {
	// Given
	command, url := "cursor-agent login", "https://cursor.com/login"
	provider := Provider{ProviderName: "Cursor", Auth: &ProviderAuth{State: "signed_out", SignInCommand: &command, SignInURL: &url}}
	// When
	output := render([]Provider{provider}, Options{}.defaults().Now(), false, map[string]warningState{})
	// Then
	if !strings.Contains(output, "Run: cursor-agent login") || !strings.Contains(output, "Sign in: https://cursor.com/login") {
		t.Fatal(output)
	}
}

func Test_CLI_render_does_not_repeat_a_command_named_by_the_error(t *testing.T) {
	// Given
	command := "cursor-agent login"
	message := "Your cursor-agent login expired. Run `cursor-agent login` again."
	provider := Provider{ProviderName: "Cursor", Error: &message, NeedsReauth: true,
		Auth: &ProviderAuth{State: "expired", SignInCommand: &command}}
	// When
	output := render([]Provider{provider}, Options{}.defaults().Now(), false, map[string]warningState{})
	// Then
	if strings.Contains(output, "Run: ") || !strings.Contains(output, "Error: "+message) {
		t.Fatal(output)
	}
}

func Test_CLI_TLS_proof_and_pinned_requests_keep_configured_proxy(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			server, sent := proofServer(t, true)
			upstreamURL, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			var tunnels atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodConnect {
					t.Errorf("unexpected proxy method %s", r.Method)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				upstream, err := net.Dial("tcp", upstreamURL.Host)
				if err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				defer upstream.Close()
				downstream, buffered, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				defer downstream.Close()
				tunnels.Add(1)
				if _, err := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
					t.Error(err)
					return
				}
				if err := buffered.Flush(); err != nil {
					t.Error(err)
					return
				}
				go func() { _, _ = io.Copy(upstream, buffered) }()
				_, _ = io.Copy(downstream, upstream)
			}))
			defer proxy.Close()
			proxyURL, err := url.Parse(proxy.URL)
			if err != nil {
				t.Fatal(err)
			}
			options := Options{Env: []string{"HEADROOM_AUTH_TOKEN=fixture-token"},
				HTTPClient: &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}}.defaults()
			defer options.HTTPClient.CloseIdleConnections()
			defer func() {
				for _, entry := range options.tlsSession.entries {
					entry.client.CloseIdleConnections()
				}
			}()
			endpoint := scheme + "://" + net.JoinHostPort("headroom-proxy-fixture.invalid", upstreamURL.Port())
			body, err := fetchHTTP(context.Background(), options, endpoint)
			if err != nil || string(body) != "[]" || sent.Load() != 1 || tunnels.Load() < 2 {
				t.Fatalf("body=%s sent=%d tunnels=%d err=%v", body, sent.Load(), tunnels.Load(), err)
			}
		})
	}
}
