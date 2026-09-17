//go:build windows

package trayhost

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"
	"unsafe"
)

func TestShellIdentityAndRecovery(t *testing.T) {
	var operations []uint32
	h := &host{window: 7, icon: 9, guid: guidForPath(`C:\Headroom\headroom.exe`)}
	h.sendShell = func(op uint32, data *notifyData) bool {
		if data.Flags&nifGuid == 0 || data.GUID != h.guid {
			t.Fatal("Shell operation lost stable identity")
		}
		if data.Size != uint32(unsafe.Sizeof(notifyData{})) {
			t.Fatal("invalid Shell structure size")
		}
		operations = append(operations, op)
		return true
	}
	if !h.updateIcon(true) || !h.added {
		t.Fatal("initial registration failed")
	}
	h.updateIcon(false)
	h.added = false // Explorer discarded its icons.
	if !h.updateIcon(true) {
		t.Fatal("Explorer recovery failed")
	}
	h.remove()
	expected := []uint32{nimAdd, nimVersion, nimModify, nimAdd, nimVersion, nimDelete}
	if len(operations) != len(expected) {
		t.Fatal(operations)
	}
	for i := range expected {
		if operations[i] != expected[i] {
			t.Fatal(operations)
		}
	}
}

func TestFailedVersionRemovesIcon(t *testing.T) {
	h := &host{window: 7, icon: 9}
	removed := false
	h.sendShell = func(op uint32, data *notifyData) bool {
		if op == nimDelete {
			removed = true
		}
		return op != nimVersion
	}
	if h.updateIcon(true) || h.added || !removed {
		t.Fatal("failed version negotiation leaked icon")
	}
}

func TestUTF16Boundary(t *testing.T) {
	var text [3]uint16
	copyText(text[:], "x😀")
	if text[0] != 'x' || text[1] != 0 {
		t.Fatal("split a surrogate pair")
	}
}

func TestNativeStructureLayout(t *testing.T) {
	// Windows x64 and ARM64 SDK layouts (both use 64-bit pointer alignment).
	if unsafe.Sizeof(notifyData{}) != 976 || unsafe.Sizeof(iconIdentifier{}) != 40 || unsafe.Sizeof(windowClass{}) != 80 || unsafe.Sizeof(message{}) != 48 {
		t.Fatalf("incorrect Win32 ABI: %d %d %d %d", unsafe.Sizeof(notifyData{}), unsafe.Sizeof(iconIdentifier{}), unsafe.Sizeof(windowClass{}), unsafe.Sizeof(message{}))
	}
}

func TestNativeLifecycle(t *testing.T) {
	if os.Getenv("HEADROOM_TRAY_NATIVE_CHILD") == "1" {
		executable, _ := os.Executable()
		if err := Run(os.Stdin, os.Stdout, executable, executable); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	if os.Getenv("HEADROOM_TRAY_NATIVE_TEST") != "1" {
		t.Skip("requires an interactive Windows Shell")
	}
	for _, closePipe := range []bool{false, true} {
		executable, _ := os.Executable()
		child := exec.Command(executable, "-test.run=^TestNativeLifecycle$")
		child.Env = append(os.Environ(), "HEADROOM_TRAY_NATIVE_CHILD=1")
		input, _ := child.StdinPipe()
		output, _ := child.StdoutPipe()
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		defer child.Process.Kill()
		encoder := json.NewEncoder(input)
		encoder.Encode(Command{Version: 1, Op: "icon", Size: 32, Pixels: make([]byte, 32*32*4)})
		ready := make(chan bool, 1)
		go func() {
			decoder := json.NewDecoder(output)
			for {
				var event Event
				if decoder.Decode(&event) != nil {
					ready <- false
					return
				}
				if event.Event == "ready" {
					ready <- true
					return
				}
			}
		}()
		select {
		case ok := <-ready:
			if !ok {
				t.Fatal("helper exited before Shell registration")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Shell registration timeout")
		}
		if !closePipe {
			encoder.Encode(Command{Version: 1, Op: "quit"})
		}
		input.Close()
		stopped := make(chan error, 1)
		go func() { stopped <- child.Wait() }()
		select {
		case err := <-stopped:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("helper did not stop on quit/EOF")
		}
	}
}
