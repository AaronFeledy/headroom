package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func fixtureUsage(t *testing.T) []byte {
	t.Helper()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	status := "120 / 500 requests"
	providers := []Provider{
		{
			ProviderName: "Codex", PrimaryLabel: "5-Hour", SecondaryLabel: "Weekly", ShowSecondary: true,
			Current: UsageBucket{Utilization: 80, ResetsAt: timePointer(now.Add(5 * time.Hour))},
			Weekly:  UsageBucket{Utilization: 31, ResetsAt: timePointer(now.Add(7 * 24 * time.Hour))},
			Buckets: []Bucket{
				{ID: "session", Label: "5-Hour", Utilization: 80, ResetsAt: timePointer(now.Add(150 * time.Minute))},
				{ID: "weekly", Label: "Weekly", Utilization: 31, ResetsAt: timePointer(now.Add(4 * 24 * time.Hour))},
				{ID: "weekly_model", Label: "Model Weekly", Utilization: 42, ResetsAt: timePointer(now.Add(5 * 24 * time.Hour)), StatusText: &status},
			},
			IsSuccess: true,
		},
	}
	encoded, err := json.Marshal(providers)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func timePointer(value time.Time) *time.Time { return &value }

func response(body []byte) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}
}

func baseOptions(body []byte) (Options, *bytes.Buffer, *bytes.Buffer) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	return Options{
		Env: []string{}, Stdout: stdout, Stderr: stderr,
		Now:        func() time.Time { return time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC) },
		IsTerminal: func(io.Writer) bool { return false },
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return response(body), nil })},
	}, stdout, stderr
}

func TestBareDashboardPrintsOnePipeFriendlySnapshot(t *testing.T) {
	body := fixtureUsage(t)
	options, stdout, _ := baseOptions(body)
	requests := 0
	options.Env = []string{"HEADROOM_AUTH_TOKEN=must-not-leave-localhost"}
	options.HTTPClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if got := request.URL.String(); got != "http://127.0.0.1:7823/api/v1/usage" {
			t.Fatalf("URL = %q", got)
		}
		if authorization := request.Header.Get("Authorization"); authorization != "" {
			t.Fatalf("local Authorization = %q", authorization)
		}
		return response(body), nil
	})
	if err := Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	for _, wanted := range []string{"ChatGPT", "5-Hour", "Weekly", "Model Weekly", "Critical", "pp over pace", "120 / 500 requests"} {
		if !strings.Contains(stdout.String(), wanted) {
			t.Errorf("output omitted %q:\n%s", wanted, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "\x1b[") {
		t.Fatalf("piped output contains terminal controls: %q", stdout.String())
	}
}

func TestExplicitRemoteUsesEnvironmentTokenAndPreservesPrefix(t *testing.T) {
	body := fixtureUsage(t)
	options, _, _ := baseOptions(body)
	options.Args = []string{"--once", "--url", "https://usage.example/prefix/"}
	options.Env = []string{"HEADROOM_AUTH_TOKEN=fixture-token"}
	options.HTTPClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.URL.String(); got != "https://usage.example/prefix/api/v1/usage" {
			t.Fatalf("URL = %q", got)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer fixture-token" {
			t.Fatalf("Authorization = %q", got)
		}
		return response(body), nil
	})
	if err := Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if err := options.defaults().HTTPClient.CheckRedirect(nil, nil); err == nil {
		t.Fatal("redirect unexpectedly allowed")
	}
}

func TestExplicitLoopbackURLUsesEnvironmentToken(t *testing.T) {
	body := fixtureUsage(t)
	options, _, _ := baseOptions(body)
	options.Args = []string{"--once", "--url", "http://127.0.0.1:7823"}
	options.Env = []string{"HEADROOM_AUTH_TOKEN=fixture-token"}
	requests := 0
	options.HTTPClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if got := request.URL.String(); got != "http://127.0.0.1:7823/api/v1/usage" {
			t.Fatalf("URL = %q", got)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer fixture-token" {
			t.Fatalf("Authorization = %q, want the explicitly selected endpoint to authenticate", got)
		}
		return response(body), nil
	})
	if err := Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestLocalIntegrationCallbackPrecedesDefaultHTTP(t *testing.T) {
	body := fixtureUsage(t)
	options, stdout, _ := baseOptions(body)
	options.Args = []string{"--once"}
	options.FetchLocal = func(context.Context) (LocalSnapshot, error) { return LocalSnapshot{Body: body}, nil }
	options.HTTPClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("default HTTP was used despite a local integration callback")
		return nil, errors.New("unreachable")
	})
	if err := Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "ChatGPT") {
		t.Fatalf("output = %q", stdout.String())
	}
}

func TestSSHUsesRestrictedProtocolAndWeeklyFilter(t *testing.T) {
	body := fixtureUsage(t)
	options, stdout, _ := baseOptions(body)
	options.Args = []string{"--ssh", "alice@example.test:2222", "--once", "--json", "--weekly-only"}
	options.RunSSH = func(_ context.Context, program string, arguments []string, input []byte) ([]byte, []byte, error) {
		if program != "ssh" {
			t.Fatalf("program = %q", program)
		}
		wantTail := []string{"-l", "alice", "-p", "2222", "--", "example.test", "usage-server --ssh-stdio"}
		if !reflect.DeepEqual(arguments[len(arguments)-len(wantTail):], wantTail) {
			t.Fatalf("SSH tail = %#v", arguments)
		}
		joined := strings.Join(arguments, " ")
		for _, option := range []string{"BatchMode=yes", "StrictHostKeyChecking=yes", "ClearAllForwardings=yes", "ForwardAgent=no", "RequestTTY=no"} {
			if !strings.Contains(joined, option) {
				t.Errorf("SSH arguments omitted %q", option)
			}
		}
		var request struct {
			Schema       int `json:"schema"`
			Method, Path string
			Body         []byte
		}
		if err := json.Unmarshal(bytes.TrimSpace(input), &request); err != nil {
			t.Fatal(err)
		}
		if request.Schema != 1 || request.Method != http.MethodGet || request.Path != "/api/v1/usage" || len(request.Body) != 0 {
			t.Fatalf("request = %+v", request)
		}
		frame, err := json.Marshal(struct {
			Schema, Status int
			Body           []byte
		}{1, http.StatusOK, body})
		if err != nil {
			t.Fatal(err)
		}
		return append(frame, '\n'), nil, nil
	}
	if err := Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	var got []Provider
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("output JSON: %v\n%s", err, stdout.String())
	}
	if len(got) != 1 || len(got[0].Buckets) != 2 || got[0].Buckets[0].ID != "weekly" || got[0].Buckets[1].ID != "weekly_model" {
		t.Fatalf("weekly providers = %+v", got)
	}
}

func TestSSHRejectsDuplicateResponseFields(t *testing.T) {
	options, _, _ := baseOptions(fixtureUsage(t))
	options.Args = []string{"--ssh", "example.test", "--once"}
	options.RunSSH = func(context.Context, string, []string, []byte) ([]byte, []byte, error) {
		return []byte(`{"schema":1,"schema":1,"status":200,"body":"W10="}` + "\n"), nil, nil
	}
	if err := Run(context.Background(), options); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("error = %v", err)
	}
}

func TestCommandCallbacksAndUnavailableErrors(t *testing.T) {
	for _, command := range []string{"serve", "update", "desktop"} {
		options, _, _ := baseOptions(fixtureUsage(t))
		options.Args = []string{command}
		if err := Run(context.Background(), options); err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Errorf("%s error = %v", command, err)
		}
	}
	options, stdout, _ := baseOptions(fixtureUsage(t))
	options.Version = "1.2.3+fixture"
	options.Args = []string{"version"}
	if err := Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "1.2.3+fixture\n" {
		t.Fatalf("version = %q", got)
	}

	called := false
	options, _, _ = baseOptions(fixtureUsage(t))
	options.Args = []string{"update", "--check"}
	options.Update = func(_ context.Context, args []string) error {
		called = reflect.DeepEqual(args, []string{"--check"})
		return nil
	}
	if err := Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("update callback did not receive arguments")
	}
}

func TestLegacyBackgroundLaunchesDesktopWithOriginalArguments(t *testing.T) {
	options, _, _ := baseOptions(fixtureUsage(t))
	options.Args = []string{"--background", "fixture"}
	var got []string
	options.Desktop = func(_ context.Context, args []string) error {
		got = append([]string(nil), args...)
		return nil
	}
	if err := Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, options.Args) {
		t.Fatalf("desktop arguments = %#v", got)
	}
}

func TestTokenFileRequiresOwnerOnlyRegularFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits are not available")
	}
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("fixture-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readToken([]string{"HEADROOM_AUTH_TOKEN_FILE=" + path})
	if err != nil || got != "fixture-token" {
		t.Fatalf("token = %q, error = %v", got, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readToken([]string{"HEADROOM_AUTH_TOKEN_FILE=" + path}); err == nil {
		t.Fatal("group-readable token accepted")
	}
}

func TestWarningHysteresisMatchesDesktopThresholds(t *testing.T) {
	noPace := pace{}
	if got := warningLevel(76, noPace, 0); got != 2 {
		t.Fatalf("entry level = %d", got)
	}
	if got := warningLevel(72, noPace, 2); got != 2 {
		t.Fatalf("recovery hysteresis level = %d", got)
	}
	if got := warningLevel(69, noPace, 2); got != 1 {
		t.Fatalf("recovered level = %d", got)
	}
	timed := pace{available: true, pressure: .41}
	if got := warningLevel(20, timed, 3); got != 3 {
		t.Fatalf("paced hysteresis level = %d", got)
	}
}

func TestOpenCodeGoDisplayNameAndJSONPassThrough(t *testing.T) {
	body := []byte(`[{"provider_name":"OpenCodeGo","provider_type":"OpenCodeGo","error":null,"is_success":true,"needs_reauth":false,"buckets":[{"id":"session","label":"5-Hour","utilization":40,"resets_at":"2026-09-11T14:30:00Z","measurement":"percent"},{"id":"weekly","label":"Weekly","utilization":20,"resets_at":"2026-09-15T00:00:00Z","measurement":"percent"},{"id":"monthly","label":"Monthly","utilization":10,"resets_at":"2026-09-30T00:00:00Z","measurement":"percent"}]}]`)
	t.Run("text", func(t *testing.T) {
		options, stdout, _ := baseOptions(body)
		if err := Run(context.Background(), options); err != nil {
			t.Fatal(err)
		}
		for _, wanted := range []string{"OpenCode Go", "5-Hour", "Weekly", "Monthly"} {
			if !strings.Contains(stdout.String(), wanted) {
				t.Fatalf("output omitted %q:\n%s", wanted, stdout.String())
			}
		}
		if strings.Contains(stdout.String(), "OpenCodeGo") {
			t.Fatalf("output kept the API name:\n%s", stdout.String())
		}
	})
	t.Run("json", func(t *testing.T) {
		options, stdout, _ := baseOptions(body)
		options.Args = []string{"--json"}
		if err := Run(context.Background(), options); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(bytes.TrimSpace(stdout.Bytes()), body) {
			t.Fatalf("JSON bytes changed: %s", stdout)
		}
	})
}
