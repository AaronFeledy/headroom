package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/tlsidentity"
)

const (
	maximumDesktopSessionInput = 512
	maximumDesktopIdentity     = 8 * 1024
	defaultDesktopInputTimeout = 5 * time.Second
)

var errInvalidDesktopSession = errors.New("invalid desktop session protocol")

type desktopSessionRequest struct {
	Schema int
	Nonce  string
	Token  string
}

type desktopSessionIdentity struct {
	Schema      int    `json:"schema"`
	Nonce       string `json:"nonce"`
	Address     string `json:"address"`
	Certificate string `json:"certificate"`
}

type desktopSessionOptions struct {
	input   io.Reader
	output  io.Writer
	random  io.Reader
	now     func() time.Time
	timeout time.Duration
	listen  func(string, string) (net.Listener, error)
	homeDir string
	version string
	ready   func() error
}

type preparedDesktopSession struct {
	listener    net.Listener
	request     desktopSessionRequest
	certificate []byte
	output      io.Writer
}

func (options desktopSessionOptions) withDefaults() desktopSessionOptions {
	if options.random == nil {
		options.random = rand.Reader
	}
	if options.now == nil {
		options.now = time.Now
	}
	if options.timeout <= 0 {
		options.timeout = defaultDesktopInputTimeout
	}
	if options.listen == nil {
		options.listen = net.Listen
	}
	if options.version == "" {
		options.version = "dev"
	}
	return options
}

func prepareDesktopSession(ctx context.Context, listenAddress string, options desktopSessionOptions) (*preparedDesktopSession, error) {
	options = options.withDefaults()
	if options.input == nil || options.output == nil {
		return nil, fmt.Errorf("desktop session I/O unavailable: %w", errInvalidDesktopSession)
	}
	request, err := readDesktopSession(ctx, options.input, options.timeout)
	if err != nil {
		return nil, err
	}
	if err := validateDesktopListenAddress(listenAddress); err != nil {
		return nil, err
	}
	listener, err := options.listen("tcp", listenAddress)
	if err != nil {
		return nil, fmt.Errorf("bind desktop session listener: %w", err)
	}
	boundAddress, ok := listener.Addr().(*net.TCPAddr)
	if !ok || boundAddress.IP == nil || !boundAddress.IP.IsLoopback() {
		listener.Close()
		return nil, fmt.Errorf("listener did not bind a numeric loopback address: %w", errInvalidDesktopSession)
	}
	certificate, tlsCertificate, err := generateDesktopCertificate(options.random, options.now(), boundAddress.IP)
	if err != nil {
		listener.Close()
		return nil, fmt.Errorf("generate desktop session certificate: %w", err)
	}
	tlsListener := tls.NewListener(listener, &tls.Config{
		Certificates: []tls.Certificate{tlsCertificate},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"http/1.1"},
	})
	return &preparedDesktopSession{
		listener:    tlsListener,
		request:     request,
		certificate: certificate,
		output:      options.output,
	}, nil
}

func readDesktopSession(ctx context.Context, input io.Reader, timeout time.Duration) (desktopSessionRequest, error) {
	type readResult struct {
		data []byte
		err  error
	}
	results := make(chan readResult, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(input, maximumDesktopSessionInput+1))
		results <- readResult{data: data, err: err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return desktopSessionRequest{}, fmt.Errorf("read desktop session input: %w", ctx.Err())
	case <-timer.C:
		return desktopSessionRequest{}, fmt.Errorf("desktop session input timed out: %w", errInvalidDesktopSession)
	case result := <-results:
		if result.err != nil {
			return desktopSessionRequest{}, fmt.Errorf("read desktop session input: %w", result.err)
		}
		return decodeDesktopSession(result.data)
	}
}

func decodeDesktopSession(data []byte) (desktopSessionRequest, error) {
	if len(data) == 0 || len(data) > maximumDesktopSessionInput || data[len(data)-1] != '\n' ||
		bytes.Count(data, []byte{'\n'}) != 1 {
		return desktopSessionRequest{}, fmt.Errorf("desktop session input is not one bounded line: %w", errInvalidDesktopSession)
	}
	line := data[:len(data)-1]
	decoder := json.NewDecoder(bytes.NewReader(line))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return desktopSessionRequest{}, fmt.Errorf("decode desktop session input: %w", errInvalidDesktopSession)
	}
	request := desktopSessionRequest{}
	seen := make(map[string]bool, 3)
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok || seen[key] {
			return desktopSessionRequest{}, fmt.Errorf("decode desktop session fields: %w", errInvalidDesktopSession)
		}
		seen[key] = true
		switch key {
		case "schema":
			err = decoder.Decode(&request.Schema)
		case "nonce":
			err = decoder.Decode(&request.Nonce)
		case "token":
			err = decoder.Decode(&request.Token)
		default:
			return desktopSessionRequest{}, fmt.Errorf("unknown desktop session field: %w", errInvalidDesktopSession)
		}
		if err != nil {
			return desktopSessionRequest{}, fmt.Errorf("decode desktop session field: %w", errInvalidDesktopSession)
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return desktopSessionRequest{}, fmt.Errorf("decode desktop session object: %w", errInvalidDesktopSession)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return desktopSessionRequest{}, fmt.Errorf("extra desktop session value: %w", errInvalidDesktopSession)
	}
	if len(seen) != 3 || request.Schema != 1 || !validLowerHex(request.Nonce, 32) || !validLowerHex(request.Token, 64) {
		return desktopSessionRequest{}, fmt.Errorf("invalid desktop session fields: %w", errInvalidDesktopSession)
	}
	return request, nil
}

func validLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	decoded := make([]byte, length/2)
	if _, err := hex.Decode(decoded, []byte(value)); err != nil {
		return false
	}
	for _, character := range value {
		if character >= 'A' && character <= 'F' {
			return false
		}
	}
	return true
}

func validateDesktopListenAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("parse desktop session listen address: %w", errInvalidDesktopSession)
	}
	ip := net.ParseIP(host)
	portNumber, portErr := strconv.ParseUint(port, 10, 16)
	if ip == nil || !ip.IsLoopback() || portErr != nil || strconv.FormatUint(portNumber, 10) != port {
		return fmt.Errorf("desktop session requires a numeric loopback listen address: %w", errInvalidDesktopSession)
	}
	return nil
}

func generateDesktopCertificate(random io.Reader, now time.Time, boundIP net.IP) ([]byte, tls.Certificate, error) {
	return tlsidentity.Generate(tlsidentity.CertificateOptions{Random: random, Now: now, BoundIP: boundIP, CommonName: "Headroom Desktop Session", Days: 365})
}

func (session *preparedDesktopSession) publishIdentity() error {
	identity := desktopSessionIdentity{
		Schema:      1,
		Nonce:       session.request.Nonce,
		Address:     session.listener.Addr().String(),
		Certificate: string(session.certificate),
	}
	encoded, err := json.Marshal(identity)
	if err != nil || len(encoded)+1 > maximumDesktopIdentity {
		return fmt.Errorf("encode desktop session identity: %w", errInvalidDesktopSession)
	}
	encoded = append(encoded, '\n')
	for len(encoded) > 0 {
		written, err := session.output.Write(encoded)
		if err != nil {
			return fmt.Errorf("write desktop session identity: %w", err)
		}
		if written <= 0 || written > len(encoded) {
			return fmt.Errorf("write desktop session identity: %w", io.ErrShortWrite)
		}
		encoded = encoded[written:]
	}
	if closer, ok := session.output.(io.Closer); ok {
		if err := closer.Close(); err != nil {
			return fmt.Errorf("close desktop session identity channel: %w", err)
		}
	}
	return nil
}
