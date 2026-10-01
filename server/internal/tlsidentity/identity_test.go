package tlsidentity

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func Test_Identity_create_reuse_rotate_and_private_modes(t *testing.T) {
	// Given
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	options := Options{Directory: filepath.Join(t.TempDir(), "tls"), Now: now, Hostname: "fixture", BoundIP: net.ParseIP("192.0.2.1")}
	// When
	first, created, err := Load(options)
	if err != nil {
		t.Fatal(err)
	}
	second, reused, err := Load(options)
	if err != nil {
		t.Fatal(err)
	}
	options.Now = now.Add(370 * 24 * time.Hour)
	third, rotated, err := Load(options)
	// Then
	if err != nil || !created || reused || !rotated || !bytes.Equal(first.Certificate[0], second.Certificate[0]) || bytes.Equal(first.Certificate[0], third.Certificate[0]) {
		t.Fatalf("rotation flags=%v/%v/%v err=%v", created, reused, rotated, err)
	}
	for path, mode := range map[string]os.FileMode{options.Directory: 0o700, filepath.Join(options.Directory, "key.pem"): 0o600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("mode=%v err=%v", info, err)
		}
	}
	leaf, err := x509.ParseCertificate(first.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if leaf.NotAfter.Sub(now) != 397*24*time.Hour || leaf.VerifyHostname("fixture") != nil || leaf.VerifyHostname("192.0.2.1") != nil {
		t.Fatal("invalid identity validity or SAN")
	}
}

func Test_Identity_regenerates_invalid_files(t *testing.T) {
	// Given
	dir := t.TempDir()
	for _, name := range []string{"cert.pem", "key.pem"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("invalid"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// When
	certificate, rotated, err := Load(Options{Directory: dir, Now: time.Now()})
	// Then
	if err != nil || !rotated || len(certificate.Certificate) != 1 {
		t.Fatalf("rotated=%v err=%v", rotated, err)
	}
}

func Test_Listener_serves_both_protocols_and_idle_connection_does_not_block(t *testing.T) {
	// Given
	certificate, _, err := Load(Options{Directory: t.TempDir(), Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := Listen(tcp, certificate)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			w.Header().Set("X-TLS", "true")
		}
		w.WriteHeader(204)
	}), ReadHeaderTimeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil && err != http.ErrServerClosed {
			t.Error(err)
		}
	})
	idle, err := net.Dial("tcp", tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := idle.Close(); err != nil {
			t.Error(err)
		}
	})
	roots := x509.NewCertPool()
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots.AddCert(leaf)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	// When
	for _, scheme := range []string{"http", "https"} {
		request, err := http.NewRequestWithContext(context.Background(), "GET", scheme+"://"+tcp.Addr().String(), nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		// Then
		if response.StatusCode != 204 || (response.Header.Get("X-TLS") == "true") != (scheme == "https") {
			t.Fatal("TLS state not preserved")
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

type temporaryError struct{}

func (temporaryError) Error() string   { return "temporary accept failure" }
func (temporaryError) Timeout() bool   { return false }
func (temporaryError) Temporary() bool { return true }

type flakyListener struct {
	net.Listener
	failures int
}

func (l *flakyListener) Accept() (net.Conn, error) {
	if l.failures > 0 {
		l.failures--
		return nil, temporaryError{}
	}
	return l.Listener.Accept()
}

func Test_Listener_recovers_from_temporary_accept_errors(t *testing.T) {
	// Given
	certificate, _, err := Load(Options{Directory: t.TempDir(), Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := Listen(&flakyListener{Listener: tcp, failures: 3}, certificate)
	t.Cleanup(func() {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Error(err)
		}
	})
	client, err := net.Dial("tcp", tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Error(err)
		}
	})
	if _, err := client.Write([]byte("G")); err != nil {
		t.Fatal(err)
	}

	// When
	accepted := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			err = conn.Close()
		}
		accepted <- err
	}()

	// Then
	select {
	case err := <-accepted:
		if err != nil {
			t.Fatalf("Accept after temporary errors: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listener stopped accepting after temporary errors")
	}
}
