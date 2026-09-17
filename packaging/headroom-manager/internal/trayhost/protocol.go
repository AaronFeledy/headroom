// Package trayhost is the private, bounded pipe protocol between the Qt desktop
// and the Windows tray icon owned by its stable launcher. It never reads usage,
// credentials, settings, or network endpoints itself.
package trayhost

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const ProtocolVersion = 1
const MaxMessageBytes = 32768

type Command struct {
	Version  int    `json:"version"`
	Op       string `json:"op"`
	Size     int    `json:"size,omitempty"`
	Pixels   []byte `json:"pixels,omitempty"` // top-down premultiplied BGRA
	Tooltip  string `json:"tooltip,omitempty"`
	Title    string `json:"title,omitempty"`
	Message  string `json:"message,omitempty"`
	Severity int    `json:"severity,omitempty"`
}

type Event struct {
	Version int    `json:"version"`
	Event   string `json:"event"`
	Size    int    `json:"size,omitempty"`
	Reason  int    `json:"reason,omitempty"`
	X       int32  `json:"x,omitempty"`
	Y       int32  `json:"y,omitempty"`
	Width   int32  `json:"width,omitempty"`
	Height  int32  `json:"height,omitempty"`
	Hover   bool   `json:"hover,omitempty"`
}

func decodeCommand(data []byte) (Command, error) {
	var command Command
	if len(data) > MaxMessageBytes {
		return command, errors.New("tray message too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&command); err != nil {
		return command, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return command, errors.New("trailing tray message")
	}
	if command.Version != ProtocolVersion {
		return command, errors.New("unsupported tray protocol")
	}
	switch command.Op {
	case "icon":
		if command.Size < 16 || command.Size > 64 || len(command.Pixels) != command.Size*command.Size*4 {
			return command, errors.New("invalid tray bitmap")
		}
	case "tooltip", "notify", "focus", "quit":
		if command.Size != 0 || len(command.Pixels) != 0 {
			return command, errors.New("unexpected tray bitmap")
		}
	default:
		return command, errors.New("unknown tray operation")
	}
	if len(command.Tooltip) > 4096 || len(command.Title) > 1024 || len(command.Message) > 4096 || command.Severity < 0 || command.Severity > 3 {
		return command, errors.New("invalid tray text")
	}
	return command, nil
}

func readCommands(input io.Reader, accept func(Command) bool) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), MaxMessageBytes+1)
	for scanner.Scan() {
		command, err := decodeCommand(scanner.Bytes())
		if err != nil {
			return err
		}
		if !accept(command) {
			return nil
		}
	}
	return scanner.Err()
}

// A GUID by itself does not survive moving an unsigned executable. Both this
// identity AND its owning launcher path must stay fixed across generations.
// Scope to the stable path so separate installations cannot steal each other's icon.
func identity(path string) [16]byte {
	path = strings.ToLower(strings.ReplaceAll(path, "/", "\\"))
	sum := sha256.Sum256([]byte("Headroom.WindowsTray.v1\x00" + path))
	var id [16]byte
	copy(id[:], sum[:16])
	id[6] = id[6]&0x0f | 0x50
	id[8] = id[8]&0x3f | 0x80
	return id
}
