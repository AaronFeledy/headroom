package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const maximumResponseBytes = 1 << 20

func fetchUsage(ctx context.Context, options Options, flags dashboardFlags) (LocalSnapshot, []Provider, error) {
	var body []byte
	var err error
	var snapshot LocalSnapshot
	if flags.ssh != "" {
		body, err = fetchSSH(ctx, options, flags.ssh)
	} else if flags.url == "" && options.FetchLocal != nil {
		snapshot, err = options.FetchLocal(ctx)
		body = snapshot.Body
		if errors.Is(err, ErrLocalUnavailable) {
			body, err = fetchHTTP(ctx, options, "")
			snapshot = LocalSnapshot{}
		}
	} else {
		body, err = fetchHTTP(ctx, options, flags.url)
	}
	if err != nil {
		return LocalSnapshot{}, nil, err
	}
	if len(body) == 0 || len(body) > maximumResponseBytes {
		return LocalSnapshot{}, nil, errors.New("usage response exceeded the size limit")
	}
	providers, err := decodeUsage(body)
	if err != nil {
		return LocalSnapshot{}, nil, err
	}
	if flags.weeklyOnly {
		for index := range providers {
			filtered := providers[index].Buckets[:0]
			for _, bucket := range providers[index].Buckets {
				if bucket.ID == "weekly" || strings.HasPrefix(bucket.ID, "weekly_") {
					filtered = append(filtered, bucket)
				}
			}
			providers[index].Buckets = filtered
		}
		body, err = filterWeeklyJSON(body)
		if err != nil {
			return LocalSnapshot{}, nil, err
		}
	}
	snapshot.Body = bytes.TrimSpace(body)
	return snapshot, providers, nil
}

func filterWeeklyJSON(body []byte) ([]byte, error) {
	var providers []map[string]json.RawMessage
	if err := json.Unmarshal(body, &providers); err != nil {
		return nil, err
	}
	for _, provider := range providers {
		var buckets []json.RawMessage
		if raw, ok := provider["buckets"]; ok {
			if err := json.Unmarshal(raw, &buckets); err != nil {
				return nil, err
			}
			filtered := make([]json.RawMessage, 0, len(buckets))
			for _, bucket := range buckets {
				var identity struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(bucket, &identity); err != nil {
					return nil, err
				}
				if identity.ID == "weekly" || strings.HasPrefix(identity.ID, "weekly_") {
					filtered = append(filtered, bucket)
				}
			}
			provider["buckets"], _ = json.Marshal(filtered)
		}
	}
	return json.Marshal(providers)
}

func fetchHTTP(ctx context.Context, options Options, base string) ([]byte, error) {
	usingDefault := base == ""
	if base == "" {
		base = "http://127.0.0.1:7823"
	}
	endpoint, err := usageURL(base)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Headroom/"+options.Version)
	// Automatic localhost discovery never sends a saved token. An address the
	// user selected explicitly may be a token-protected loopback server.
	token := ""
	if !usingDefault {
		token, err = readToken(options.Env)
		if err != nil {
			return nil, err
		}
	}
	var response *http.Response
	if token != "" {
		response, err = authenticatedRequest(ctx, options, request, token)
	} else {
		response, err = options.HTTPClient.Do(request)
	}
	if err != nil {
		if usingDefault {
			return nil, fmt.Errorf("local Headroom usage is unavailable; start 'headroom serve' or use --url/--ssh: %w", err)
		}
		return nil, fmt.Errorf("fetch usage: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseBytes+1))
	if err != nil || len(body) > maximumResponseBytes {
		return nil, errors.New("usage response exceeded the size limit")
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("usage server returned HTTP %d", response.StatusCode)
	}
	return body, nil
}

func usageURL(base string) (string, error) {
	parsed, err := url.ParseRequestURI(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("--url must be an HTTP(S) base URL without credentials, query, or fragment")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/api/v1/usage"
	return parsed.String(), nil
}

func readToken(env []string) (string, error) {
	direct, file := envValue(env, "HEADROOM_AUTH_TOKEN"), envValue(env, "HEADROOM_AUTH_TOKEN_FILE")
	if direct != "" && file != "" {
		return "", errors.New("set only one of HEADROOM_AUTH_TOKEN and HEADROOM_AUTH_TOKEN_FILE")
	}
	if direct != "" {
		if len(direct) > 64*1024 || strings.ContainsAny(direct, "\r\n\x00") {
			return "", errors.New("HEADROOM_AUTH_TOKEN is invalid or too long")
		}
		return direct, nil
	}
	if file == "" {
		return "", nil
	}
	if runtime.GOOS == "windows" {
		return "", errors.New("use HEADROOM_AUTH_TOKEN on Windows; token files require verified owner-only permissions")
	}
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return "", errors.New("token file is missing or unsafe")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return "", errors.New("token file must be readable only by its owner")
	}
	handle, err := os.Open(file)
	if err != nil {
		return "", errors.New("could not read token file")
	}
	defer handle.Close()
	openedInfo, err := handle.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) || openedInfo.Size() > 64*1024 {
		return "", errors.New("token file changed while it was opened")
	}
	data, err := io.ReadAll(io.LimitReader(handle, 64*1024+1))
	if err != nil || len(data) > 64*1024 {
		return "", errors.New("could not read token file")
	}
	token := strings.TrimSuffix(string(data), "\n")
	if token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return "", errors.New("token file must contain one nonempty line")
	}
	return token, nil
}

type sshDestination struct {
	host, user string
	port       int
}

func parseSSH(value string) (sshDestination, error) {
	if value == "" || strings.TrimSpace(value) != value || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return sshDestination{}, errors.New("--ssh contains whitespace or control characters")
	}
	if !strings.Contains(value, "://") {
		value = "ssh://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "ssh" || parsed.Hostname() == "" || parsed.Path != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil && parsed.User.String() != parsed.User.Username() {
		return sshDestination{}, errors.New("--ssh must be [user@]host[:port] without a password or path")
	}
	port := 0
	if parsed.Port() != "" {
		port64, err := strconv.ParseUint(parsed.Port(), 10, 16)
		if err != nil || port64 == 0 {
			return sshDestination{}, errors.New("--ssh contains an invalid port")
		}
		port = int(port64)
	}
	validName := func(value string) bool {
		for index, character := range value {
			if !(character == '_' || character == '-' || character == '.' || character >= '0' && character <= '9' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z') || index == 0 && (character == '-' || character == '.') {
				return false
			}
		}
		return value != ""
	}
	if net.ParseIP(parsed.Hostname()) == nil && !validName(parsed.Hostname()) {
		return sshDestination{}, errors.New("--ssh contains an invalid host")
	}
	user := ""
	if parsed.User != nil {
		user = parsed.User.Username()
		if !validName(user) {
			return sshDestination{}, errors.New("--ssh contains an invalid user")
		}
	}
	return sshDestination{host: parsed.Hostname(), user: user, port: port}, nil
}

func fetchSSH(ctx context.Context, options Options, destination string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	target, err := parseSSH(destination)
	if err != nil {
		return nil, err
	}
	arguments := []string{"-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "PermitLocalCommand=no",
		"-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "ClearAllForwardings=yes", "-o", "ForwardAgent=no",
		"-o", "ForwardX11=no", "-o", "RemoteCommand=none", "-o", "RequestTTY=no", "-o", "ConnectTimeout=8",
		"-o", "ConnectionAttempts=1"}
	if target.user != "" {
		arguments = append(arguments, "-l", target.user)
	}
	if target.port != 0 {
		arguments = append(arguments, "-p", strconv.Itoa(target.port))
	}
	arguments = append(arguments, "--", target.host, "usage-server --ssh-stdio")
	frame, _ := json.Marshal(struct {
		Schema int    `json:"schema"`
		Method string `json:"method"`
		Path   string `json:"path"`
		Body   []byte `json:"body"`
	}{1, http.MethodGet, "/api/v1/usage", []byte{}})
	frame = append(frame, '\n')
	runner := options.RunSSH
	if runner == nil {
		runner = runSSH
	}
	stdout, stderr, err := runner(ctx, "ssh", arguments, frame)
	if err != nil {
		return nil, fmt.Errorf("SSH usage request failed: %w", err)
	}
	if len(stdout) == 0 || len(stdout) > 2*maximumResponseBytes || len(stderr) > 2*maximumResponseBytes ||
		stdout[len(stdout)-1] != '\n' || bytes.Count(stdout, []byte{'\n'}) != 1 {
		return nil, errors.New("SSH usage response was invalid")
	}
	var response struct {
		Schema int    `json:"schema"`
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	encodedResponse := stdout[:len(stdout)-1]
	if validateUniqueJSON(encodedResponse) != nil {
		return nil, errors.New("SSH usage response was invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(encodedResponse))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&response) != nil || response.Schema != 1 || response.Status < 100 || response.Status > 599 {
		return nil, errors.New("SSH usage response was invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("SSH usage response was invalid")
	}
	body, err := base64.StdEncoding.Strict().DecodeString(response.Body)
	if err != nil || len(body) > maximumResponseBytes || base64.StdEncoding.EncodeToString(body) != response.Body {
		return nil, errors.New("SSH usage response was invalid")
	}
	if response.Status != http.StatusOK {
		return nil, fmt.Errorf("SSH usage server returned HTTP %d", response.Status)
	}
	return body, nil
}

func runSSH(ctx context.Context, program string, arguments []string, input []byte) ([]byte, []byte, error) {
	path, err := exec.LookPath(program)
	if err != nil {
		return nil, nil, errors.New("system OpenSSH client was not found")
	}
	command := exec.CommandContext(ctx, path, arguments...)
	// OpenSSH can forward environment variables through user SendEnv settings.
	// HTTP credentials are never part of the SSH transport.
	command.Env = make([]string, 0, len(os.Environ()))
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		switch strings.ToUpper(key) {
		case "HEADROOM_AUTH_TOKEN", "HEADROOM_AUTH_TOKEN_FILE", "USAGE_AUTH_TOKEN":
			continue
		}
		command.Env = append(command.Env, item)
	}
	command.Stdin = bytes.NewReader(input)
	var stdout, stderr boundedBuffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

type boundedBuffer struct{ bytes.Buffer }

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	if buffer.Len()+len(data) > 2*maximumResponseBytes {
		return 0, errors.New("SSH output exceeded the size limit")
	}
	return buffer.Buffer.Write(data)
}
