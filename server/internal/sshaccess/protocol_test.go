//go:build linux

package sshaccess

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/api"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func validFrame(method, path string, body []byte) []byte {
	return []byte(`{"schema":1,"method":"` + method + `","path":"` + path + `","body":"` + base64.StdEncoding.EncodeToString(body) + `"}` + "\n")
}

func TestDecodeRequestAcceptsOnlyExactProtocol(t *testing.T) {
	request, err := decodeRequest(validFrame(http.MethodPut, "/api/v1/providers/grok/credentials", []byte(`{"cookie":"secret"}`)))
	if err != nil || string(request.Body) != `{"cookie":"secret"}` {
		t.Fatalf("decodeRequest = %#v, %v", request, err)
	}

	tests := map[string][]byte{
		"missing newline":  validFrame("GET", "/api/v1/health", nil)[:len(validFrame("GET", "/api/v1/health", nil))-1],
		"double frame":     append(validFrame("GET", "/api/v1/health", nil), validFrame("GET", "/api/v1/health", nil)...),
		"trailing data":    append(validFrame("GET", "/api/v1/health", nil), 'x'),
		"duplicate":        []byte(`{"schema":1,"schema":1,"method":"GET","path":"/api/v1/health","body":""}` + "\n"),
		"unknown":          []byte(`{"schema":1,"method":"GET","path":"/api/v1/health","body":"","x":1}` + "\n"),
		"missing":          []byte(`{"schema":1,"method":"GET","path":"/api/v1/health"}` + "\n"),
		"null body":        []byte(`{"schema":1,"method":"GET","path":"/api/v1/health","body":null}` + "\n"),
		"wrong type":       []byte(`{"schema":1,"method":7,"path":"/api/v1/health","body":""}` + "\n"),
		"schema":           []byte(`{"schema":2,"method":"GET","path":"/api/v1/health","body":""}` + "\n"),
		"noncanonical b64": []byte(`{"schema":1,"method":"PUT","path":"/api/v1/providers/grok/credentials","body":"Zh=="}` + "\n"),
		"get body":         validFrame("GET", "/api/v1/health", []byte("x")),
		"unknown path":     validFrame("GET", "/api/v1/usage/cursor", nil),
		"wrong method":     validFrame("POST", "/api/v1/usage", nil),
		"oversized":        append(bytes.Repeat([]byte{'x'}, maximumFrameBytes), '\n'),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeRequest(input); !errors.Is(err, errInvalidProtocol) {
				t.Fatalf("decodeRequest error = %v", err)
			}
		})
	}
}

func TestServeStdioUsesProtectedSocketAndSameHandler(t *testing.T) {
	home := trustedHome(t)
	listener, err := Listen(home)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer listener.Close()
	state := "before"
	handler := InjectAuthorization("token", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPut {
			body, _ := io.ReadAll(r.Body)
			state = string(body)
		}
		_, _ = io.WriteString(w, state)
	}))
	server := &http.Server{Handler: handler}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()

	var output bytes.Buffer
	if err := ServeStdio(context.Background(), bytes.NewReader(validFrame("PUT", "/api/v1/providers/grok/credentials", []byte("after"))), &output, home); err != nil {
		t.Fatalf("ServeStdio PUT: %v", err)
	}
	response := decodeResponse(t, output.Bytes())
	if response.Status != http.StatusOK || string(response.Body) != "after" || state != "after" {
		t.Fatalf("response = %#v state=%q", response, state)
	}
	output.Reset()
	if err := ServeStdio(context.Background(), bytes.NewReader(validFrame("GET", "/api/v1/usage", nil)), &output, home); err != nil {
		t.Fatalf("ServeStdio GET: %v", err)
	}
	if got := decodeResponse(t, output.Bytes()); string(got.Body) != "after" {
		t.Fatalf("GET body = %q", got.Body)
	}
	_ = server.Close()
	<-serverDone
}

func TestServeStdioCredentialUpdateRefetchesSameAPIStateServedOverTCP(t *testing.T) {
	home := trustedHome(t)
	listener, err := Listen(home)
	if err != nil {
		t.Fatal(err)
	}
	provider := &statefulCursor{cookie: "before"}
	cache := poller.New(poller.Options{})
	if err := cache.Register(provider, true); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := cache.PollProvider(context.Background(), "Cursor"); err != nil || !ok {
		t.Fatalf("initial poll ok=%v err=%v", ok, err)
	}
	handler := api.NewHandler(api.Options{Cache: cache, Cursor: provider, Poller: cache, AuthToken: "configured-secret", ProviderNames: []string{"Cursor"}})
	unixServer := &http.Server{Handler: InjectAuthorization("configured-secret", handler)}
	done := make(chan error, 1)
	go func() { done <- unixServer.Serve(listener) }()
	tcpServer := httptest.NewServer(handler)
	defer tcpServer.Close()

	var output bytes.Buffer
	body := []byte(`{"cookie":"after"}`)
	if err := ServeStdio(context.Background(), bytes.NewReader(validFrame("PUT", "/api/v1/providers/cursor/credentials", body)), &output, home); err != nil {
		t.Fatal(err)
	}
	if response := decodeResponse(t, output.Bytes()); response.Status != http.StatusOK {
		t.Fatalf("SSH PUT status = %d body=%s", response.Status, response.Body)
	}
	request, _ := http.NewRequest(http.MethodGet, tcpServer.URL+"/api/v1/usage", nil)
	request.Header.Set("Authorization", "Bearer configured-secret")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !bytes.Contains(data, []byte(`"utilization":22`)) {
		t.Fatalf("TCP usage status=%d body=%s", response.StatusCode, data)
	}
	if bytes.Contains(output.Bytes(), []byte("configured-secret")) {
		t.Fatal("SSH response exposed configured bearer token")
	}
	_ = unixServer.Close()
	<-done
}

type statefulCursor struct {
	mu     sync.Mutex
	cookie string
}

func (provider *statefulCursor) Name() string { return "Cursor" }

func (provider *statefulCursor) SetCookieHeader(cookie string) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.cookie = cookie
}

func (provider *statefulCursor) SetDesktopCookie(cookie, sourceName string) {
	provider.SetCookieHeader(cookie)
}

func (provider *statefulCursor) SetAccessToken(string) error { return nil }

func (provider *statefulCursor) Fetch(context.Context) (usage.UsageData, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	value := float64(11)
	if provider.cookie == "after" {
		value = 22
	}
	return usage.FromBuckets("Cursor", []usage.Bucket{{ID: "session", Label: "Current", Utilization: value}}), nil
}

func TestServeStdioMalformedFrameWritesOneGenericResponse(t *testing.T) {
	var output bytes.Buffer
	if err := ServeStdio(context.Background(), strings.NewReader("{}\n{}\n"), &output, trustedHome(t)); err != nil {
		t.Fatalf("ServeStdio: %v", err)
	}
	if bytes.Count(output.Bytes(), []byte{'\n'}) != 1 {
		t.Fatalf("output = %q", output.Bytes())
	}
	response := decodeResponse(t, output.Bytes())
	if response.Status != http.StatusBadRequest || response.Body == nil || strings.Contains(string(response.Body), "socket") {
		t.Fatalf("response = %#v", response)
	}
}

func TestServeStdioRejectsWeakSocketAndDirectoryPermissions(t *testing.T) {
	for _, target := range []string{"socket", "directory"} {
		t.Run(target, func(t *testing.T) {
			home := trustedHome(t)
			listener, err := Listen(home)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			path := SocketPath(home)
			if target == "socket" {
				err = os.Chmod(path, 0o660)
			} else {
				err = os.Chmod(filepath.Dir(path), 0o755)
			}
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := ServeStdio(context.Background(), bytes.NewReader(validFrame("GET", "/api/v1/health", nil)), &output, home); err != nil {
				t.Fatal(err)
			}
			if response := decodeResponse(t, output.Bytes()); response.Status != http.StatusBadGateway {
				t.Fatalf("status = %d", response.Status)
			}
		})
	}
}

func TestTrustedPeerRejectsDifferentUIDAndLookupFailure(t *testing.T) {
	if trustedPeer(nil, 1000, func(*net.UnixConn) (uint32, error) { return 1001, nil }) {
		t.Fatal("accepted different UID")
	}
	if trustedPeer(nil, 1000, func(*net.UnixConn) (uint32, error) { return 1000, errors.New("lookup") }) {
		t.Fatal("accepted failed peer lookup")
	}
}

func TestReadBoundedHonorsCancellation(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readBounded(ctx, reader, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("readBounded error = %v", err)
	}
}

func TestListenRejectsUnsafePathsAndLiveCollision(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		home := trustedHome(t)
		target := t.TempDir()
		if err := os.Symlink(target, filepath.Join(home, ".local")); err != nil {
			t.Fatal(err)
		}
		if _, err := Listen(home); err == nil {
			t.Fatal("Listen accepted symlink")
		}
	})
	t.Run("writable ancestor", func(t *testing.T) {
		home := trustedHome(t)
		if err := os.Mkdir(filepath.Join(home, ".local"), 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(home, ".local"), 0o777); err != nil {
			t.Fatal(err)
		}
		if _, err := Listen(home); err == nil {
			t.Fatal("Listen accepted writable directory")
		}
	})
	t.Run("live collision", func(t *testing.T) {
		home := trustedHome(t)
		first, err := Listen(home)
		if err != nil {
			t.Fatal(err)
		}
		defer first.Close()
		if _, err := Listen(home); err == nil {
			t.Fatal("second Listen accepted live socket")
		}
	})
}

func TestListenRecoversStaleSocketAndDoesNotRemoveReplacement(t *testing.T) {
	home := trustedHome(t)
	sshDir := filepath.Dir(SocketPath(home))
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	address, _ := net.ResolveUnixAddr("unix", SocketPath(home))
	stale, err := net.ListenUnix("unix", address)
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := os.Chmod(SocketPath(home), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = stale.Close()
	owned, err := Listen(home)
	if err != nil {
		t.Fatalf("recover stale: %v", err)
	}
	moved := SocketPath(home) + ".old"
	if err := os.Rename(SocketPath(home), moved); err != nil {
		t.Fatal(err)
	}
	replacement, err := net.ListenUnix("unix", address)
	if err != nil {
		t.Fatal(err)
	}
	replacement.SetUnlinkOnClose(false)
	defer func() { replacement.Close(); os.Remove(SocketPath(home)); os.Remove(moved) }()
	if err := owned.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(SocketPath(home)); err != nil {
		t.Fatalf("replacement removed: %v", err)
	}
}

func trustedHome(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "hrssh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	return home
}

func decodeResponse(t *testing.T, data []byte) responseFrame {
	t.Helper()
	var response responseFrame
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Schema != 1 {
		t.Fatalf("response schema = %d", response.Schema)
	}
	return response
}
