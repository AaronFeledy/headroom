package cli

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var errTLSProof = errors.New("TLS identity proof failed")
var errTLSPin = errors.New("TLS certificate pin changed")

type tlsSession struct {
	mu      sync.Mutex
	entries map[string]*tlsEntry
}
type tlsEntry struct {
	endpoint    string
	client      *http.Client
	pin         []byte
	lastUpgrade time.Time
}

func proofMessage(token, nonce, fingerprint string) []byte {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("headroom-tls-proof-v1\n" + nonce + "\n" + fingerprint))
	return mac.Sum(nil)
}

// Keep the configured proxy and dial policy when adding proof or pin verification.
func tlsTransport(original *http.Client) *http.Transport {
	transport, ok := original.Transport.(*http.Transport)
	if !ok || transport == nil {
		transport = http.DefaultTransport.(*http.Transport)
	}
	return transport.Clone()
}

func proveTLS(ctx context.Context, original *http.Client, endpoint, token string) ([]byte, error) {
	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, fmt.Errorf("TLS nonce: %w", err)
	}
	nonce := hex.EncodeToString(nonceBytes)
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/api/v1/usage") + "/api/v1/tls/proof"
	parsed.RawQuery = "nonce=" + nonce
	transport := tlsTransport(original)
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}
	transport.DisableKeepAlives = true
	transport.TLSHandshakeTimeout = 2 * time.Second
	transport.ResponseHeaderTimeout = 2 * time.Second
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errTLSProof }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("TLS proof request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.TLS == nil || len(response.TLS.PeerCertificates) == 0 {
		return nil, errTLSProof
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(body) > 4096 {
		return nil, errTLSProof
	}
	var proof struct {
		CertificateSHA256 string `json:"certificate_sha256"`
		Proof             string `json:"proof"`
	}
	if err := json.Unmarshal(body, &proof); err != nil {
		return nil, errTLSProof
	}
	der := response.TLS.PeerCertificates[0].Raw
	hash := sha256.Sum256(der)
	fingerprint := hex.EncodeToString(hash[:])
	provided, err := hex.DecodeString(proof.Proof)
	if err != nil || proof.CertificateSHA256 != fingerprint || !hmac.Equal(provided, proofMessage(token, nonce, fingerprint)) {
		return nil, errTLSProof
	}
	return bytes.Clone(der), nil
}

func pinnedClient(original *http.Client, der []byte) (*http.Client, error) {
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse TLS pin: %w", err)
	}
	name := ""
	if len(certificate.DNSNames) > 0 {
		name = certificate.DNSNames[0]
	} else if len(certificate.IPAddresses) > 0 {
		name = certificate.IPAddresses[0].String()
	}
	if name == "" {
		return nil, errTLSProof
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	transport := tlsTransport(original)
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: name, MinVersion: tls.VersionTLS12, VerifyConnection: func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 || !bytes.Equal(state.PeerCertificates[0].Raw, der) {
			return errTLSPin
		}
		return nil
	}}
	transport.TLSHandshakeTimeout = 3 * time.Second
	client := *original
	client.Transport = transport
	return &client, nil
}

func authenticatedRequest(ctx context.Context, options Options, request *http.Request, token string) (*http.Response, error) {
	session := options.tlsSession
	if session == nil {
		session = &tlsSession{entries: map[string]*tlsEntry{}}
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	hash := sha256.Sum256([]byte(token))
	key := request.URL.String() + hex.EncodeToString(hash[:])
	entry := session.entries[key]
	if entry == nil {
		entry = &tlsEntry{endpoint: request.URL.String(), client: options.HTTPClient}
		session.entries[key] = entry
	}
	now := time.Now()
	if options.Now != nil {
		now = options.Now()
	}
	if request.URL.Scheme == "http" && len(entry.pin) == 0 && (entry.lastUpgrade.IsZero() || now.Sub(entry.lastUpgrade) >= 6*time.Hour) {
		entry.lastUpgrade = now
		upgrade := *request.URL
		upgrade.Scheme = "https"
		if upgrade.Port() == "" {
			upgrade.Host = net.JoinHostPort(upgrade.Hostname(), "80")
		}
		if pin, err := proveTLS(ctx, options.HTTPClient, upgrade.String(), token); err == nil {
			client, err := pinnedClient(options.HTTPClient, pin)
			if err != nil {
				return nil, err
			}
			entry.endpoint, entry.pin, entry.client = upgrade.String(), pin, client
		}
	}
	target, err := url.Parse(entry.endpoint)
	if err != nil {
		return nil, err
	}
	request = request.Clone(ctx)
	request.URL = target
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := entry.client.Do(request)
	if err == nil {
		return response, nil
	}
	if target.Scheme != "https" {
		return nil, err
	}
	var verification *tls.CertificateVerificationError
	if !errors.As(err, &verification) && !errors.Is(err, errTLSPin) {
		return nil, err
	}
	pin, proofErr := proveTLS(ctx, options.HTTPClient, target.String(), token)
	if proofErr != nil {
		return nil, fmt.Errorf("verify server TLS identity: %w", proofErr)
	}
	client, pinErr := pinnedClient(options.HTTPClient, pin)
	if pinErr != nil {
		return nil, pinErr
	}
	entry.pin, entry.client = pin, client
	return client.Do(request)
}
