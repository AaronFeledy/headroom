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
	"strconv"
	"strings"
	"time"
	"unicode"
)

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
