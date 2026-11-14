package contract

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

var activationTools struct {
	sync.Once
	manager, fixture string
	err              error
}

func installActivationTools(t *testing.T) (string, string) {
	t.Helper()
	activationTools.Do(func() {
		directory, err := os.MkdirTemp("", "headroom-activation-tools-")
		if err != nil {
			activationTools.err = err
			return
		}
		activationTools.manager = filepath.Join(directory, "headroom-package"+nativeExtension())
		activationTools.fixture = filepath.Join(directory, "headroom-install-fixture"+nativeExtension())
		for _, tool := range []struct{ path, pkg string }{
			{activationTools.manager, "../cmd/headroom-manager"}, {activationTools.fixture, "../cmd/headroom-install-fixture"},
		} {
			command := exec.Command("go", "build", "-p", "2", "-ldflags=-s -w", "-o", tool.path, tool.pkg)
			if data, err := command.CombinedOutput(); err != nil {
				activationTools.err = &fixtureBuildError{err, string(data)}
				return
			}
		}
	})
	if activationTools.err != nil {
		t.Fatal(activationTools.err)
	}
	return activationTools.manager, activationTools.fixture
}

type fixtureBuildError struct {
	err    error
	output string
}

func (e *fixtureBuildError) Error() string { return e.err.Error() + ": " + e.output }

func activationArchive(t *testing.T, version, kind string) string {
	t.Helper()
	manager, fixture := installActivationTools(t)
	command := exec.Command(fixture, "package", "--output", t.TempDir(), "--manager", manager, "--version", version, "--kind", kind)
	data, err := command.Output()
	if err != nil {
		var message string
		if exit, ok := err.(*exec.ExitError); ok {
			message = string(exit.Stderr)
		}
		t.Fatalf("fixture package: %v %s", err, message)
	}
	var result struct {
		Archive string `json:"archive"`
	}
	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result.Archive
}

func activationInstall(t *testing.T, archive string) (string, InstallResult) {
	t.Helper()
	directory := t.TempDir()
	root := filepath.Join(directory, "install with spaces")
	entry := filepath.Join(directory, "entry", "headroom-gui"+nativeExtension())
	manager, _ := installActivationTools(t)
	data, err := exec.Command(manager, "install", "--archive", archive, "--install-root", root, "--entry-path", entry).Output()
	var reply struct {
		OK     bool          `json:"ok"`
		Result InstallResult `json:"result"`
	}
	if err != nil || json.Unmarshal(data, &reply) != nil || !reply.OK {
		t.Fatalf("install command = %s, %v", data, err)
	}
	result := reply.Result
	t.Cleanup(func() { stopActivationFixture(t, root) })
	return root, result
}

func stopActivationFixture(t *testing.T, root string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "fixture-primary.json"))
	if os.IsNotExist(err) {
		return
	}
	var info desktopInfo
	if err != nil || json.Unmarshal(data, &info) != nil {
		t.Errorf("read primary for cleanup: %v", err)
		return
	}
	if err = os.WriteFile(filepath.Join(root, "fixture-stop-"+strconv.Itoa(info.PID)), nil, 0o600); err != nil {
		t.Error(err)
		return
	}
	token, err := captureProcessToken(info.PID, info.Executable)
	if err != nil {
		return
	}
	watch, err := watchProcess(info.PID, info.Executable, token)
	if err != nil {
		return
	}
	defer watch.Close()
	if err = watch.Wait(3 * time.Second); err != nil {
		t.Error(err)
	}
}

func TestInstallActivation(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("synthetic AppKit wrapper requires native package CI")
	}
	archive := activationArchive(t, "41.0.0", "")
	// Successful activation uses the command's normal readiness budget, including delayed markers.
	for _, scenario := range []struct{ name, mode string }{
		{"fresh command ready", ""},
		{"delayed", "delayed"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Setenv("HEADROOM_FIXTURE_READY_MODE", scenario.mode)
			root, installed := activationInstall(t, archive)
			if installed.State == nil || installed.Activation != nil {
				t.Fatalf("direct install result = %+v", installed)
			}
			manager, _ := installActivationTools(t)
			data, err := exec.Command(manager, "activate", "--install-root", root, "--version-path", installed.State.VersionPath).Output()
			var reply struct {
				OK     bool             `json:"ok"`
				Result ActivationResult `json:"result"`
			}
			if err != nil || json.Unmarshal(data, &reply) != nil || !reply.OK || reply.Result.Status != "ready" || reply.Result.VersionPath != installed.State.VersionPath {
				t.Fatalf("activation command = %s, %v", data, err)
			}
		})
	}
	t.Run("no launch", func(t *testing.T) {
		root, installed := activationInstall(t, archive)
		result, err := ActivateInstall(context.Background(), ActivationOptions{InstallRoot: root, VersionPath: installed.State.VersionPath, NoLaunch: true})
		if err != nil || result.Status != "skipped" || result.Reason != "launch_disabled" {
			t.Fatalf("activation = %+v, %v", result, err)
		}
		if _, err = os.Stat(filepath.Join(root, "fixture-primary.json")); !os.IsNotExist(err) {
			t.Fatalf("no-launch started app: %v", err)
		}
	})
	t.Run("wrong generation", func(t *testing.T) {
		root, _ := activationInstall(t, archive)
		if _, err := ActivateInstall(context.Background(), ActivationOptions{InstallRoot: root, VersionPath: "versions/41.0.0"}); err == nil {
			t.Fatal("accepted unselected generation")
		}
	})
	t.Run("old bridge is not absence", func(t *testing.T) {
		root, installed := activationInstall(t, archive)
		startActivationPrimary(t, root)
		t.Setenv("HEADROOM_FIXTURE_OLD_BRIDGE", "1")
		result, err := ActivateInstall(context.Background(), ActivationOptions{InstallRoot: root, VersionPath: installed.State.VersionPath})
		if err != nil || result.Status != "unconfirmed" {
			t.Fatalf("old bridge activation = %+v, %v", result, err)
		}
	})
	t.Run("cli skipped", func(t *testing.T) {
		root, installed := activationInstall(t, activationArchive(t, "41.0.0", PackageKindCLI))
		if installed.Activation == nil || installed.Activation.Status != "skipped" || installed.Activation.Reason != "cli_only" {
			t.Fatalf("CLI result = %+v", installed)
		}
		result, err := ActivateInstall(context.Background(), ActivationOptions{InstallRoot: root, VersionPath: installed.State.VersionPath})
		if err != nil || result.Status != "skipped" {
			t.Fatalf("CLI activation = %+v, %v", result, err)
		}
	})
	for _, mode := range []string{"wrong_nonce", "wrong_pid", "wrong_path", "wrong_version", "early_exit", "missing", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			root, installed := activationInstall(t, archive)
			t.Setenv("HEADROOM_FIXTURE_READY_MODE", mode)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result, err := ActivateInstall(ctx, ActivationOptions{InstallRoot: root, VersionPath: installed.State.VersionPath})
			if err != nil || result.Status != "unconfirmed" {
				t.Fatalf("activation = %+v %v, want unconfirmed", result, err)
			}
		})
	}
}

func startActivationPrimary(t *testing.T, root string) {
	t.Helper()
	executable, _, err := ActiveExecutable(root)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable)
	command.Env = append(os.Environ(), "HEADROOM_INSTALL_ROOT="+root)
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		stopActivationFixture(t, root)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("fixture did not exit")
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		if data, err := os.ReadFile(filepath.Join(root, "fixture-primary.json")); err == nil && len(data) > 0 {
			return
		}
		if err = observePause(ctx); err != nil {
			t.Fatal("primary fixture did not become ready")
		}
	}
}
