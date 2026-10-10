package sshaccess

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
	"strings"
	"time"
)

const (
	maximumFrameBytes = 2 << 20
	maximumBodyBytes  = 1 << 20
	stdioTimeout      = 10 * time.Second
)

var errInvalidProtocol = errors.New("invalid SSH access request")

type requestFrame struct {
	Schema int
	Method string
	Path   string
	Body   []byte
}

type responseFrame struct {
	Schema int    `json:"schema"`
	Status int    `json:"status"`
	Body   []byte `json:"body"`
}

// InjectAuthorization adds the daemon's configured bearer token inside the
// trusted socket endpoint. No token is exposed to the SSH receiver process.
func InjectAuthorization(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del("Authorization")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		next.ServeHTTP(w, r)
	})
}

// ServeStdio handles one bounded request frame and writes one response frame.
// Protocol and transport failures are intentionally returned as generic HTTP
// error bodies so credentials and local filesystem details never reach stderr.
func ServeStdio(ctx context.Context, input io.Reader, output io.Writer, home string) error {
	data, err := readBounded(ctx, input, stdioTimeout)
	if err != nil {
		return writeResponse(output, http.StatusBadRequest, []byte(`{"error":"invalid request"}`))
	}
	request, err := decodeRequest(data)
	if err != nil {
		return writeResponse(output, http.StatusBadRequest, []byte(`{"error":"invalid request"}`))
	}
	status, body, err := doRequest(ctx, home, request)
	if err != nil {
		return writeResponse(output, http.StatusBadGateway, []byte(`{"error":"SSH access unavailable"}`))
	}
	return writeResponse(output, status, body)
}

func readBounded(ctx context.Context, input io.Reader, timeout time.Duration) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	results := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(input, maximumFrameBytes+1))
		results <- result{data: data, err: err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, errInvalidProtocol
	case result := <-results:
		if result.err != nil {
			return nil, errInvalidProtocol
		}
		return result.data, nil
	}
}

func decodeRequest(data []byte) (requestFrame, error) {
	if len(data) == 0 || len(data) > maximumFrameBytes || data[len(data)-1] != '\n' || bytes.Count(data, []byte{'\n'}) != 1 {
		return requestFrame{}, errInvalidProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(data[:len(data)-1]))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return requestFrame{}, errInvalidProtocol
	}
	var request requestFrame
	var encodedBody string
	seen := make(map[string]bool, 4)
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok || seen[key] {
			return requestFrame{}, errInvalidProtocol
		}
		seen[key] = true
		switch key {
		case "schema":
			err = decoder.Decode(&request.Schema)
		case "method":
			request.Method, err = decodeJSONString(decoder)
		case "path":
			request.Path, err = decodeJSONString(decoder)
		case "body":
			encodedBody, err = decodeJSONString(decoder)
		default:
			return requestFrame{}, errInvalidProtocol
		}
		if err != nil {
			return requestFrame{}, errInvalidProtocol
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || len(seen) != 4 || request.Schema != 1 {
		return requestFrame{}, errInvalidProtocol
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return requestFrame{}, errInvalidProtocol
	}
	body, err := base64.StdEncoding.Strict().DecodeString(encodedBody)
	if err != nil || len(body) > maximumBodyBytes || base64.StdEncoding.EncodeToString(body) != encodedBody {
		return requestFrame{}, errInvalidProtocol
	}
	request.Body = body
	if !allowedRequest(request) {
		return requestFrame{}, errInvalidProtocol
	}
	return request, nil
}

func decodeJSONString(decoder *json.Decoder) (string, error) {
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil || len(raw) < 2 || raw[0] != '"' {
		return "", errInvalidProtocol
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", errInvalidProtocol
	}
	return value, nil
}

func allowedRequest(request requestFrame) bool {
	switch request.Method + " " + request.Path {
	case "GET /api/v1/health", "GET /api/v1/usage", "POST /api/v1/usage/refresh":
		return len(request.Body) == 0
	case "PUT /api/v1/providers/cursor/credentials", "PUT /api/v1/providers/grok/credentials":
		return true
	// This route can spend a very valuable banked reset. Do not test the reset
	// button, endpoint, or code that may trigger it. Its skipped test is intentional.
	case "POST /api/v1/providers/codex/reset":
		return true
	default:
		return false
	}
}

func doRequest(ctx context.Context, home string, request requestFrame) (int, []byte, error) {
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
			return dialTrustedSocket(dialCtx, home)
		},
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	requestTimeout := 10 * time.Second
	if request.Method == http.MethodPost && request.Path == "/api/v1/providers/codex/reset" {
		requestTimeout = 70 * time.Second
	}
	client := &http.Client{Transport: transport, Timeout: requestTimeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return errors.New("redirect disabled")
	}}
	httpRequest, err := http.NewRequestWithContext(ctx, request.Method, "http://headroom"+request.Path, bytes.NewReader(request.Body))
	if err != nil {
		return 0, nil, err
	}
	if request.Method == http.MethodPost && request.Path == "/api/v1/providers/codex/reset" {
		// DO NOT test this path. The reset upload may be sent only once,
		// including when a socket connection fails before a reply arrives.
		httpRequest.GetBody = nil
	}
	if request.Method == http.MethodPut || request.Method == http.MethodPost {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumBodyBytes+1))
	if err != nil || len(body) > maximumBodyBytes {
		return 0, nil, errInvalidProtocol
	}
	return response.StatusCode, body, nil
}

func writeResponse(output io.Writer, status int, body []byte) error {
	if body == nil {
		body = []byte{}
	}
	encoded, err := json.Marshal(responseFrame{Schema: 1, Status: status, Body: body})
	if err != nil || len(encoded)+1 > maximumFrameBytes {
		return fmt.Errorf("encode SSH response")
	}
	encoded = append(encoded, '\n')
	for len(encoded) > 0 {
		written, err := output.Write(encoded)
		if err != nil {
			return fmt.Errorf("write SSH response")
		}
		if written <= 0 || written > len(encoded) {
			return fmt.Errorf("write SSH response")
		}
		encoded = encoded[written:]
	}
	return nil
}

func IsStdioMode(args []string) (bool, error) {
	found := false
	for _, arg := range args {
		if strings.TrimLeft(arg, "-") == "ssh-stdio" {
			found = true
		}
	}
	if !found {
		return false, nil
	}
	if len(args) != 1 || (args[0] != "--ssh-stdio" && args[0] != "-ssh-stdio") {
		return false, fmt.Errorf("--ssh-stdio must be used alone")
	}
	return true, nil
}
