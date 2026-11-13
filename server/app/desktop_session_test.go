package app

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
)

const (
	testDesktopNonce = "00112233445566778899aabbccddeeff"
	testDesktopToken = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
)

func validDesktopInput() []byte {
	return []byte(`{"schema":1,"nonce":"` + testDesktopNonce + `","token":"` + testDesktopToken + `"}` + "\n")
}

func Test_DecodeDesktopSession_accepts_exact_protocol(t *testing.T) {
	request, err := decodeDesktopSession(validDesktopInput())
	if err != nil {
		t.Fatalf("decodeDesktopSession: %v", err)
	}
	if request.Schema != 1 || request.Nonce != testDesktopNonce || request.Token != testDesktopToken {
		t.Fatalf("request = %#v", request)
	}
}

func Test_DecodeDesktopSession_rejects_noncanonical_input(t *testing.T) {
	tests := map[string][]byte{
		"empty":           nil,
		"missing newline": validDesktopInput()[:len(validDesktopInput())-1],
		"extra frame":     append(validDesktopInput(), []byte("{}\n")...),
		"trailing byte":   append(validDesktopInput(), 'x'),
		"malformed":       []byte("{\n"),
		"unknown field":   []byte(`{"schema":1,"nonce":"` + testDesktopNonce + `","token":"` + testDesktopToken + `","extra":true}` + "\n"),
		"missing field":   []byte(`{"schema":1,"nonce":"` + testDesktopNonce + `"}` + "\n"),
		"duplicate field": []byte(`{"schema":1,"schema":1,"nonce":"` + testDesktopNonce + `","token":"` + testDesktopToken + `"}` + "\n"),
		"wrong schema":    []byte(`{"schema":2,"nonce":"` + testDesktopNonce + `","token":"` + testDesktopToken + `"}` + "\n"),
		"wrong type":      []byte(`{"schema":1,"nonce":7,"token":"` + testDesktopToken + `"}` + "\n"),
		"uppercase nonce": []byte(`{"schema":1,"nonce":"00112233445566778899AABBCCDDEEFF","token":"` + testDesktopToken + `"}` + "\n"),
		"short token":     []byte(`{"schema":1,"nonce":"` + testDesktopNonce + `","token":"00"}` + "\n"),
		"oversized":       append(bytes.Repeat([]byte{'x'}, maximumDesktopSessionInput), '\n'),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := decodeDesktopSession(input)
			if !errors.Is(err, errInvalidDesktopSession) {
				t.Fatalf("error = %v, want errInvalidDesktopSession", err)
			}
			if strings.Contains(err.Error(), testDesktopToken) {
				t.Fatal("error disclosed desktop token")
			}
		})
	}
}

func Test_ReadDesktopSession_times_out_on_unclosed_input(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	_, err := readDesktopSession(context.Background(), reader, 10*time.Millisecond)
	if !errors.Is(err, errInvalidDesktopSession) {
		t.Fatalf("error = %v, want errInvalidDesktopSession", err)
	}
}

func Test_RunDesktopSession_rejects_off_loopback_before_provider_construction(t *testing.T) {
	args := []string{"-config", filepath.Join(t.TempDir(), "missing.yaml"), "-desktop-session", "-listen-addr", "0.0.0.0:0"}
	env := []string{"USAGE_PROVIDER_UNKNOWN_ENABLED=true"}
	err := runContext(context.Background(), args, env, discardLogger(), desktopSessionOptions{
		input: bytes.NewReader(validDesktopInput()), output: io.Discard,
	})
	if !errors.Is(err, errInvalidDesktopSession) {
		t.Fatalf("error = %v, want errInvalidDesktopSession", err)
	}
}

func Test_RunDesktopSession_bind_failure_publishes_no_identity_before_provider_construction(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer listener.Close()
	var output bytes.Buffer
	args := []string{"-config", filepath.Join(t.TempDir(), "missing.yaml"), "-desktop-session", "-listen-addr", listener.Addr().String()}
	env := []string{"USAGE_PROVIDER_UNKNOWN_ENABLED=true"}
	err = runContext(context.Background(), args, env, discardLogger(), desktopSessionOptions{
		input: bytes.NewReader(validDesktopInput()), output: &output,
	})
	if err == nil || errors.Is(err, config.ErrInvalidConfig) {
		t.Fatalf("error = %v, want bind failure before provider construction", err)
	}
	if output.Len() != 0 {
		t.Fatalf("published identity on bind failure: %q", output.Bytes())
	}
}

func Test_RunDesktopSession_serves_pinned_authenticatedTLS(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outputReader, outputWriter := io.Pipe()
	defer outputReader.Close()
	result := make(chan error, 1)
	args := []string{"-config", filepath.Join(t.TempDir(), "missing.yaml"), "-desktop-session", "-listen-addr", "127.0.0.1:0"}
	go func() {
		result <- runContext(ctx, args, disabledProviderEnv(), discardLogger(), desktopSessionOptions{
			input: bytes.NewReader(validDesktopInput()), output: outputWriter,
		})
		outputWriter.Close()
	}()

	identityResult := make(chan struct {
		line []byte
		err  error
	}, 1)
	go func() {
		line, err := bufio.NewReader(outputReader).ReadBytes('\n')
		identityResult <- struct {
			line []byte
			err  error
		}{line: line, err: err}
	}()
	var line []byte
	var err error
	select {
	case identity := <-identityResult:
		line, err = identity.line, identity.err
	case runErr := <-result:
		t.Fatalf("desktop server exited before identity: %v", runErr)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for desktop identity")
	}
	if err != nil {
		t.Fatalf("read identity: %v", err)
	}
	if len(line) > maximumDesktopIdentity || bytes.Count(line, []byte{'\n'}) != 1 {
		t.Fatalf("identity framing length=%d", len(line))
	}
	var identity desktopSessionIdentity
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&identity); err != nil {
		t.Fatalf("decode identity: %v", err)
	}
	if identity.Schema != 1 || identity.Nonce != testDesktopNonce || net.ParseIP(hostFromAddress(t, identity.Address)) == nil {
		t.Fatalf("identity = %#v", identity)
	}
	if bytes.Contains(line, []byte(testDesktopToken)) || bytes.Contains(line, []byte("PRIVATE KEY")) {
		t.Fatal("desktop identity disclosed private session material")
	}
	block, _ := pemDecodeCertificate(t, identity.Certificate)
	certificate, err := x509.ParseCertificate(block)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	if err := certificate.VerifyHostname("127.0.0.1"); err != nil {
		t.Fatalf("verify IPv4 SAN: %v", err)
	}
	if err := certificate.VerifyHostname("::1"); err != nil {
		t.Fatalf("verify IPv6 SAN: %v", err)
	}
	if err := certificate.VerifyHostname("localhost"); err != nil {
		t.Fatalf("verify localhost SAN: %v", err)
	}

	rootCAs := x509.NewCertPool()
	if !rootCAs.AppendCertsFromPEM([]byte(identity.Certificate)) {
		t.Fatal("append desktop certificate")
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		Proxy: nil, TLSClientConfig: &tls.Config{
			RootCAs: rootCAs, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12,
		},
	}}
	endpoint := "https://" + identity.Address + "/api/v1/health"
	untrustedClient := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	if _, err := untrustedClient.Get(endpoint); err == nil {
		t.Fatal("untrusted system roots accepted desktop certificate")
	}
	if status := healthStatus(t, client, endpoint, "wrong-token"); status != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d", status)
	}
	if status := healthStatus(t, client, endpoint, testDesktopToken); status != http.StatusOK {
		t.Fatalf("authenticated health status = %d", status)
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("runContext after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("desktop server did not stop")
	}
}

func Test_GenerateDesktopCertificate_includes_bound_loopback_and_apple_compatible_validity(t *testing.T) {
	now := time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)
	certificatePEM, _, err := generateDesktopCertificate(rand.Reader, now, net.ParseIP("127.0.0.2"))
	if err != nil {
		t.Fatalf("generateDesktopCertificate: %v", err)
	}
	der, _ := pemDecodeCertificate(t, string(certificatePEM))
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	for _, hostname := range []string{"127.0.0.1", "127.0.0.2", "::1", "localhost"} {
		if err := certificate.VerifyHostname(hostname); err != nil {
			t.Fatalf("verify SAN %s: %v", hostname, err)
		}
	}
	if got, want := certificate.NotBefore, now.Add(-5*time.Minute); !got.Equal(want) {
		t.Fatalf("NotBefore = %s, want %s", got, want)
	}
	if got, want := certificate.NotAfter, now.AddDate(0, 0, 365); !got.Equal(want) {
		t.Fatalf("NotAfter = %s, want %s", got, want)
	}
	if validity := certificate.NotAfter.Sub(certificate.NotBefore); validity > 825*24*time.Hour {
		t.Fatalf("certificate validity %s exceeds Apple's app-anchored TLS limit", validity)
	}
}

func Test_Run_ordinaryStartup_remainsPlainHTTP(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listenerReady := make(chan net.Listener, 1)
	result := make(chan error, 1)
	var output bytes.Buffer
	options := desktopSessionOptions{
		input: bytes.NewReader(nil), output: &output,
		listen: func(network, address string) (net.Listener, error) {
			listener, err := net.Listen(network, address)
			if err == nil {
				listenerReady <- listener
			}
			return listener, err
		},
	}
	args := []string{"-config", filepath.Join(t.TempDir(), "missing.yaml"), "-listen-addr", "127.0.0.1:0"}
	go func() { result <- runContext(ctx, args, disabledProviderEnv(), discardLogger(), options) }()
	var listener net.Listener
	select {
	case listener = <-listenerReady:
	case err := <-result:
		t.Fatalf("ordinary server exited before listen: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ordinary listener")
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	response, err := client.Get("http://" + listener.Addr().String() + "/api/v1/health")
	if err != nil {
		t.Fatalf("ordinary HTTP health: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("ordinary HTTP status = %d", response.StatusCode)
	}
	if output.Len() != 0 {
		t.Fatalf("ordinary startup wrote desktop identity: %q", output.Bytes())
	}
	cancel()
	if err := <-result; err != nil {
		t.Fatalf("ordinary runContext after cancel: %v", err)
	}
}

func disabledProviderEnv() []string {
	return []string{
		"USAGE_PROVIDER_CLAUDE_ENABLED=false",
		"USAGE_PROVIDER_CODEX_ENABLED=false",
		"USAGE_PROVIDER_CURSOR_ENABLED=false",
		"USAGE_PROVIDER_GROK_ENABLED=false",
		"USAGE_PROVIDER_OPENCODEGO_ENABLED=false",
	}
}

func hostFromAddress(t *testing.T, address string) string {
	t.Helper()
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("split identity address: %v", err)
	}
	return host
}

func pemDecodeCertificate(t *testing.T, certificate string) ([]byte, []byte) {
	t.Helper()
	block, rest := pem.Decode([]byte(certificate))
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		t.Fatal("identity certificate is not one PEM certificate")
	}
	return block.Bytes, rest
}

func healthStatus(t *testing.T, client *http.Client, endpoint, token string) int {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatalf("new health request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("health request: %v", err)
	}
	defer response.Body.Close()
	io.Copy(io.Discard, response.Body)
	return response.StatusCode
}
