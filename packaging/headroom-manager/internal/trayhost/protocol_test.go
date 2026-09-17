package trayhost

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestStableIdentity(t *testing.T) {
	a := identity(`C:\Users\Test\Headroom\headroom.exe`)
	if a != identity(`c:/users/test/headroom/headroom.exe`) {
		t.Fatal("path case or separator changed identity")
	}
	if a == identity(`C:\Users\Other\Headroom\headroom.exe`) {
		t.Fatal("separate installations share an identity")
	}
	if got := hex.EncodeToString(a[:]); got != "7974f8089c125c1c98618b11a2f2235f" {
		t.Fatalf("desktop NIM_DELETE identity drifted: %s", got)
	}
}

func TestCommands(t *testing.T) {
	valid := Command{Version: 1, Op: "icon", Size: 64, Pixels: make([]byte, 64*64*4)}
	data, _ := json.Marshal(valid)
	if _, err := decodeCommand(data); err != nil {
		t.Fatal(err)
	}
	invalid := []string{
		`{"version":2,"op":"quit"}`, `{"version":1,"op":"execute"}`,
		`{"version":1,"op":"icon","size":64,"pixels":"AA=="}`,
		`{"version":1,"op":"icon","size":2147483647}`, `{"version":1,"op":"quit","extra":true}`,
		`{"version":1,"op":"quit"} {}`, `{"version":1,"op":"notify","severity":4}`,
		strings.Repeat(" ", MaxMessageBytes+1),
	}
	for _, value := range invalid {
		if _, err := decodeCommand([]byte(value)); err == nil {
			t.Errorf("accepted invalid command %.80q", value)
		}
	}
}

func TestPipeTerminationAndBounds(t *testing.T) {
	input := bytes.NewBufferString("{\"version\":1,\"op\":\"quit\"}\ninvalid\n")
	count := 0
	if err := readCommands(input, func(c Command) bool { count++; return false }); err != nil || count != 1 {
		t.Fatalf("quit: %d %v", count, err)
	}
	if err := readCommands(strings.NewReader(strings.Repeat("x", MaxMessageBytes+2)), func(Command) bool { t.Fatal("oversized command delivered"); return true }); err == nil {
		t.Fatal("accepted oversized pipe input")
	}
	if err := readCommands(strings.NewReader(""), func(Command) bool { t.Fatal("empty pipe delivered command"); return true }); err != nil {
		t.Fatal(err)
	}
}
