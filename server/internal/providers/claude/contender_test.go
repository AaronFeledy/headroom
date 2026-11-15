package claude

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const (
	properLockfileFixtureEnv = "HEADROOM_PROPER_LOCKFILE_FIXTURE"
	wslLockFixtureEnv        = "HEADROOM_WSL_LOCK_FIXTURE"
	contenderReadyTimeout    = 30 * time.Second
	contenderExitTimeout     = 30 * time.Second
)

func contenderPath(t *testing.T) string {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("testdata", "lockfile-interop", "contender.js"))
	if err != nil {
		t.Fatalf("resolve contender script: %v", err)
	}
	return script
}

// lockedBuffer collects a child process's stderr without racing its reader.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// contender drives a long-lived Node lock holder: it waits for stdout markers
// and releases the holder by closing stdin.
type contender struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan string
	stderr *lockedBuffer
	wait   chan error
	done   bool
}

func startContender(t *testing.T, argv []string, args ...string) *contender {
	t.Helper()
	command := exec.Command(argv[0], append(append([]string{}, argv[1:]...), args...)...)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatalf("contender stdin: %v", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("contender stdout: %v", err)
	}
	stderr := &lockedBuffer{}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start contender %v: %v", argv, err)
	}
	c := &contender{t: t, cmd: command, stdin: stdin, lines: make(chan string, 8), stderr: stderr, wait: make(chan error, 1)}
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			c.lines <- scanner.Text()
		}
		close(c.lines)
	}()
	go func() {
		c.wait <- command.Wait()
	}()
	t.Cleanup(func() {
		if c.done {
			return
		}
		if err := c.cmd.Process.Kill(); err != nil {
			t.Logf("kill contender in cleanup: %v", err)
		}
		<-c.wait
	})
	return c
}

func (c *contender) expect(marker string) {
	c.t.Helper()
	select {
	case line, ok := <-c.lines:
		if !ok {
			c.t.Fatalf("contender exited before %q; stderr: %s", marker, c.stderr.String())
		}
		if line != marker {
			c.t.Fatalf("contender said %q, want %q; stderr: %s", line, marker, c.stderr.String())
		}
	case <-time.After(contenderReadyTimeout):
		c.t.Fatalf("timed out waiting for %q; stderr: %s", marker, c.stderr.String())
	}
}

func (c *contender) release() {
	c.t.Helper()
	if err := c.stdin.Close(); err != nil {
		c.t.Fatalf("close contender stdin: %v", err)
	}
	c.expect("RELEASED")
	c.finish()
}

// kill terminates the holder without running its exit cleanup, leaving its lock
// directories behind for staleness tests.
func (c *contender) kill() {
	c.t.Helper()
	if err := c.cmd.Process.Kill(); err != nil {
		c.t.Fatalf("kill contender: %v", err)
	}
	select {
	case <-c.wait:
		c.done = true
	case <-time.After(contenderExitTimeout):
		c.t.Fatalf("contender did not exit after kill; stderr: %s", c.stderr.String())
	}
}

func (c *contender) finish() {
	c.t.Helper()
	select {
	case err := <-c.wait:
		c.done = true
		if err != nil {
			c.t.Fatalf("contender exit: %v; stderr: %s", err, c.stderr.String())
		}
	case <-time.After(contenderExitTimeout):
		c.t.Fatalf("contender did not exit; stderr: %s", c.stderr.String())
	}
}

func runContender(t *testing.T, argv []string, args ...string) string {
	t.Helper()
	command := exec.Command(argv[0], append(append([]string{}, argv[1:]...), args...)...)
	stderr := &lockedBuffer{}
	command.Stderr = stderr
	out, err := command.Output()
	if err != nil {
		t.Fatalf("contender %v: %v; stderr: %s", args, err, stderr.String())
	}
	return string(bytes.TrimSpace(out))
}

// waitForStale polls until path's mtime is older than stale. Staleness timing is
// the behavior under test here, so this waits on the clock instead of sleeping a
// fixed interval.
func waitForStale(t *testing.T, path string, stale time.Duration) {
	t.Helper()
	deadline := time.Now().Add(contenderExitTimeout)
	for {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if time.Since(info.ModTime()) > stale {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not become stale", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
