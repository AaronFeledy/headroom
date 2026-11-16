//go:build windows

package contract

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestReplaceAtomicWaitsForReaderToClose(t *testing.T) {
	directory := t.TempDir()
	source, destination := filepath.Join(directory, "source"), filepath.Join(directory, "destination")
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	attempts := 0
	err = retryWhileFileInUse(func() error {
		attempts++
		err := moveFileReplacing(source, destination)
		if attempts == 1 {
			if !fileInUse(err) {
				t.Fatalf("replace under an open reader = %v, want a file-in-use error", err)
			}
			if closeErr := reader.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
		}
		return err
	}, fileInUseRetryTimeout)

	if err != nil || attempts != 2 {
		t.Fatalf("replace = %v after %d attempts, want success on the second", err, attempts)
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "new" {
		t.Fatalf("destination = %q, %v", data, err)
	}
}

func TestRetryWhileFileInUseStopsOnOtherErrorsAndAtTimeout(t *testing.T) {
	other := errors.New("other failure")
	attempts := 0
	if err := retryWhileFileInUse(func() error { attempts++; return other }, time.Second); !errors.Is(err, other) || attempts != 1 {
		t.Fatalf("other error = %v after %d attempts, want one attempt", err, attempts)
	}

	attempts = 0
	err := retryWhileFileInUse(func() error { attempts++; return windows.ERROR_SHARING_VIOLATION }, 20*time.Millisecond)
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) || attempts < 2 {
		t.Fatalf("persistent sharing violation = %v after %d attempts, want retries then the error", err, attempts)
	}
}
