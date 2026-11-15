package credstore_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

func Test_Refresh_writes_PID_and_UNIX_seconds_to_lock_file(t *testing.T) {
	// Given
	path := writeCreds(t, `{"sentinel":"keep"}`, 0o600)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	lockHeld := make(chan struct{})
	releaseLock := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- credstore.Refresh(ctx, path, func(current []byte) (credstore.Mutator, error) {
			close(lockHeld)
			<-releaseLock
			return func(latest []byte) ([]byte, error) {
				return latest, nil
			}, nil
		})
	}()
	<-lockHeld

	// When
	metadata, err := os.ReadFile(filepath.Clean(path) + ".lock")
	close(releaseLock)

	// Then
	if updateErr := <-done; updateErr != nil {
		t.Fatalf("Refresh: %v", updateErr)
	}
	if err != nil {
		t.Fatalf("read lock metadata: %v", err)
	}
	want := regexp.MustCompile(`^[0-9]+:[0-9]+$`)
	if !want.Match(metadata) {
		t.Fatalf("lock metadata = %q, want PID:UNIX_SECONDS without newline", metadata)
	}
	pidText, secondsText, _ := strings.Cut(string(metadata), ":")
	pid, err := strconv.Atoi(pidText)
	if err != nil || pid != os.Getpid() {
		t.Fatalf("lock PID = %q, want %d", pidText, os.Getpid())
	}
	seconds, err := strconv.ParseInt(secondsText, 10, 64)
	if err != nil || seconds <= 0 {
		t.Fatalf("lock UNIX_SECONDS = %q, want positive integer", secondsText)
	}
}
