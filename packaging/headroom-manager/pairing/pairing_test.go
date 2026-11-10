package pairing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/contract"
)

func fixtureConfig() Config {
	return Config{Schema: 1, Product: "Headroom", ID: strings.Repeat("a", 48), State: "active", WindowsEntry: `C:\Users\Example\Headroom\cli\headroom.exe`, Distribution: "Test Distro", User: "testuser", LinuxEntry: "/home/testuser/.local/bin/headroom"}
}

type fixtureEndpoint struct {
	name                             string
	identity                         Identity
	events                           *[]string
	prepareErr, commitErr, verifyErr error
	current                          bool
}

func (f *fixtureEndpoint) Inspect(context.Context) (Identity, error) {
	*f.events = append(*f.events, f.name+".inspect")
	return f.identity, nil
}
func (f *fixtureEndpoint) Prepare(_ context.Context, version string) (Prepared, error) {
	*f.events = append(*f.events, f.name+".prepare:"+version)
	operation := strings.Repeat("b", 48)
	if f.current {
		operation = ""
	}
	return Prepared{Version: version, Operation: operation, Current: f.current}, f.prepareErr
}
func (f *fixtureEndpoint) Commit(context.Context, Prepared) error {
	*f.events = append(*f.events, f.name+".commit")
	return f.commitErr
}
func (f *fixtureEndpoint) Verify(_ context.Context, version string) error {
	*f.events = append(*f.events, f.name+".verify:"+version)
	return f.verifyErr
}

func fixtureCoordinator() (Coordinator, *fixtureEndpoint, *fixtureEndpoint, *[]string) {
	config := fixtureConfig()
	events := []string{}
	local := &fixtureEndpoint{name: "local", events: &events, identity: Identity{Version: "2.0.0", Platform: "windows", Architecture: "x86_64", Entry: config.WindowsEntry, Root: `C:\Headroom`, Manager: `C:\Headroom\versions\2.0.0\bin\headroom-package.exe`, Trusted: true}}
	peer := &fixtureEndpoint{name: "peer", events: &events, identity: Identity{Version: "2.0.0", Platform: "linux", Architecture: "x86_64", Kind: "cli", Entry: config.LinuxEntry, Root: "/home/testuser/.local/share/headroom", Manager: "/home/testuser/.local/share/headroom/versions/2.0.0/bin/headroom-package", Trusted: true}}
	c := Coordinator{Config: config, Platform: "windows", Local: local, Peer: peer, Latest: func(context.Context) (string, error) { return "2.1.0", nil }, SaveProgress: func(p Progress) error { events = append(events, "save:"+string(p.Phase)); return nil }}
	return c, local, peer, &events
}

func TestBothSidesPrepareBeforePeerThenLocalCommit(t *testing.T) {
	c, _, _, events := fixtureCoordinator()
	version, err := c.Run(context.Background())
	if err != nil || version != "2.1.0" {
		t.Fatalf("%s %v", version, err)
	}
	want := []string{"local.inspect", "peer.inspect", "local.prepare:2.1.0", "peer.prepare:2.1.0", "save:prepared", "save:peer_committing", "peer.commit", "peer.verify:2.1.0", "save:peer_applied", "local.commit"}
	if !reflect.DeepEqual(*events, want) {
		t.Fatalf("events %v", *events)
	}
}

func TestPreparationFailureNeverCommitsEitherSide(t *testing.T) {
	c, _, peer, events := fixtureCoordinator()
	peer.prepareErr = errors.New("unavailable")
	if _, err := c.Run(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	for _, event := range *events {
		if strings.Contains(event, "commit") {
			t.Fatal(*events)
		}
	}
}

func TestUncertainPeerResultMustVerifyBeforeLocalCommit(t *testing.T) {
	for _, verified := range []bool{false, true} {
		c, _, peer, events := fixtureCoordinator()
		peer.commitErr = errors.New("response lost")
		if !verified {
			peer.verifyErr = errors.New("not ready")
		}
		_, err := c.Run(context.Background())
		if (err == nil) != verified {
			t.Fatalf("verified=%v err=%v", verified, err)
		}
		committed := false
		for _, event := range *events {
			committed = committed || event == "local.commit"
		}
		if committed != verified {
			t.Fatal(*events)
		}
	}
}

func TestPartialRecoveryRetainsExactVersion(t *testing.T) {
	c, local, peer, events := fixtureCoordinator()
	peer.identity.Version = "2.1.0"
	peer.current = true
	c.ReadProgress = func() (Progress, error) {
		return Progress{Schema: 1, Product: "Headroom", PairingID: c.Config.ID, Version: "2.1.0", Phase: "peer_applied", Peer: &peer.identity}, nil
	}
	c.Latest = func(context.Context) (string, error) { t.Fatal("must resume exact target"); return "", nil }
	local.commitErr = errors.New("failed")
	if _, err := c.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "local update still needs") {
		t.Fatal(err)
	}
	for _, event := range *events {
		if event == "peer.commit" {
			t.Fatal(*events)
		}
	}
}

func TestNewerPeerCannotBeDowngraded(t *testing.T) {
	c, _, peer, events := fixtureCoordinator()
	peer.identity.Version = "2.2.0"
	if _, err := c.Run(context.Background()); err == nil {
		t.Fatal("downgrade accepted")
	}
	if len(*events) != 2 {
		t.Fatal(*events)
	}
}

func TestAlreadyCurrentPairVerifiesBoth(t *testing.T) {
	c, local, peer, events := fixtureCoordinator()
	local.identity.Version = "2.1.0"
	peer.identity.Version = "2.1.0"
	if _, err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"local.inspect", "peer.inspect", "local.verify:2.1.0", "peer.verify:2.1.0", "save:complete"}
	if !reflect.DeepEqual(*events, want) {
		t.Fatal(*events)
	}
}

func TestPairProtocolRejectsDuplicateUnknownAndTrailingData(t *testing.T) {
	for _, body := range []string{`{"schema":1,"schema":2}`, `{"unknown":1}`, `{} {}`, strings.Repeat("x", MaximumMessage+1)} {
		var request Request
		if Decode(strings.NewReader(body), &request) == nil {
			t.Fatalf("accepted %q", body[:min(len(body), 80)])
		}
	}
}

func TestNativeArgumentsNeverEnterAShell(t *testing.T) {
	config := fixtureConfig()
	config.Distribution = "Distro & test"
	config.LinuxEntry = "/home/testuser/space $HOME ; test/headroom"
	r := Runner{Config: config, Platform: "windows", WSLExecutable: `C:\Windows\System32\wsl.exe`}
	program, args, err := r.command(context.Background(), config.LinuxEntry, []string{RPCArgument})
	want := []string{"--distribution", config.Distribution, "--user", config.User, "--exec", config.LinuxEntry, RPCArgument}
	if err != nil || program != r.WSLExecutable || !reflect.DeepEqual(args, want) {
		t.Fatalf("%s %v %v", program, args, err)
	}
	r.Platform = "linux"
	r.TranslateWindows = func(_ context.Context, path string) (string, error) {
		if path != config.WindowsEntry {
			t.Fatal(path)
		}
		return "/mnt/c/space & test/headroom.exe", nil
	}
	program, args, err = r.command(context.Background(), config.WindowsEntry, []string{RPCArgument})
	if err != nil || program != "/mnt/c/space & test/headroom.exe" || !reflect.DeepEqual(args, []string{RPCArgument}) {
		t.Fatal(program, args, err)
	}
}

func TestPeerReplyNonceMustMatch(t *testing.T) {
	r := Runner{Config: fixtureConfig(), Platform: "windows", WSLExecutable: "wsl.exe", Execute: func(_ context.Context, _ string, _ []string, input []byte) ([]byte, error) {
		var request Request
		if Decode(bytes.NewReader(input), &request) != nil {
			t.Fatal("invalid request")
		}
		return []byte(`{"schema":1,"product":"Headroom","ok":true,"nonce":"wrong","pairing_id":"` + request.PairingID + `"}`), nil
	}}
	if _, err := r.Call(context.Background(), Request{Command: "inspect"}); err == nil {
		t.Fatal("accepted wrong nonce")
	}
}

func TestRunnerVerifyRequiresTerminalAppliedOutcome(t *testing.T) {
	config := fixtureConfig()
	identity := Identity{Version: "2.0.0", Platform: "linux", Architecture: "x86_64", Kind: contract.PackageKindCLI,
		Entry: config.LinuxEntry, Root: "/home/testuser/.local/share/headroom", Manager: "/home/testuser/.local/share/headroom/versions/2.0.0/bin/headroom-package", Trusted: true}
	status := ""
	runner := Runner{Config: config, Platform: "windows", WSLExecutable: "wsl.exe", identity: identity,
		Execute: func(_ context.Context, _ string, _ []string, _ []byte) ([]byte, error) {
			reply := struct {
				OK      bool                `json:"ok"`
				Command string              `json:"command"`
				Result  contract.Inspection `json:"result"`
			}{OK: true, Command: "inspect", Result: contract.Inspection{Version: "2.1.0", Platform: "linux", Architecture: "x86_64", PackageKind: contract.PackageKindCLI, TrustedIdentity: true, Complete: true, ApplyStatus: status}}
			return json.Marshal(reply)
		}}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runner.Verify(cancelled, "2.1.0"); err == nil {
		t.Fatal("state switch without terminal apply result was accepted")
	}
	status = "applied"
	if err := runner.Verify(context.Background(), "2.1.0"); err != nil {
		t.Fatalf("terminal applied result was rejected: %v", err)
	}
}

func TestNativeCommitBindsPrivateStageToPairAndOperation(t *testing.T) {
	root := t.TempDir()
	if canonical, err := filepath.EvalSymlinks(root); err == nil {
		root = canonical
	}
	config := fixtureConfig()
	result := contract.UpdateResult{Status: "staged", Version: "2.1.0", Stage: &contract.StageResult{Version: "2.1.0", PackageRoot: "/fixture/stage/package"}}
	operation := strings.Repeat("c", 48)
	if err := contract.WritePrivateJSON(root, preparedName, preparedRecord{Schema: 1, Product: "Headroom", PairingID: config.ID, Operation: operation, Result: result}); err != nil {
		t.Fatal(err)
	}
	called := false
	native := Native{Root: root, Config: config, ApplyFunc: func(_ context.Context, got contract.UpdateResult) error {
		called = reflect.DeepEqual(got, result)
		return nil
	}}
	if err := native.Commit(context.Background(), Prepared{Version: "2.1.0", Operation: strings.Repeat("d", 48)}); err == nil || called {
		t.Fatal("mismatched private operation was accepted")
	}
	if err := native.Commit(context.Background(), Prepared{Version: "2.1.0", Operation: operation}); err != nil || !called {
		t.Fatalf("bound private operation failed: called=%v err=%v", called, err)
	}
}

func TestExplicitReconciliationStagesCommonNewerTargetBeforeCommit(t *testing.T) {
	c, local, peer, events := fixtureCoordinator()
	previous := Progress{Schema: 1, Product: "Headroom", PairingID: c.Config.ID, Version: "2.1.0", Phase: "peer_applied", Peer: &peer.identity}
	c.ReadProgress = func() (Progress, error) { return previous, nil }
	peer.identity.Version = "2.2.0"
	c.Latest = func(context.Context) (string, error) { return "2.3.0", nil }
	c.Reconcile = true
	version, err := c.Run(context.Background())
	if err != nil || version != "2.3.0" {
		t.Fatal(version, err)
	}
	want := []string{"local.inspect", "peer.inspect", "local.verify:" + local.identity.Version, "peer.verify:2.2.0", "local.prepare:2.3.0", "peer.prepare:2.3.0", "save:prepared", "save:peer_committing", "peer.commit", "peer.verify:2.3.0", "save:peer_applied", "local.commit"}
	if !reflect.DeepEqual(*events, want) {
		t.Fatal(*events)
	}
}

func TestReconciliationFailurePreservesUnfinishedProgress(t *testing.T) {
	for _, mode := range []string{"peer-unready", "peer-stage-fails", "older-catalog"} {
		t.Run(mode, func(t *testing.T) {
			c, _, peer, events := fixtureCoordinator()
			c.Reconcile = true
			c.ReadProgress = func() (Progress, error) {
				return Progress{Schema: 1, Product: "Headroom", PairingID: c.Config.ID, Version: "2.1.0", Phase: "peer_applied", Peer: &peer.identity}, nil
			}
			switch mode {
			case "peer-unready":
				peer.verifyErr = errors.New("unready")
			case "peer-stage-fails":
				peer.prepareErr = errors.New("failed")
			case "older-catalog":
				c.Latest = func(context.Context) (string, error) { return "2.0.0", nil }
			}
			if _, err := c.Run(context.Background()); err == nil {
				t.Fatal("expected failure")
			}
			for _, event := range *events {
				if strings.HasPrefix(event, "save:") || strings.Contains(event, "commit") {
					t.Fatal("changed unfinished progress or committed", *events)
				}
			}
		})
	}
}

func TestPinnedRecoveryErrorNamesExactVersion(t *testing.T) {
	c, _, peer, _ := fixtureCoordinator()
	c.ReadProgress = func() (Progress, error) {
		return Progress{Schema: 1, Product: "Headroom", PairingID: c.Config.ID, Version: "2.1.0", Phase: "peer_applied", Peer: &peer.identity}, nil
	}
	peer.verifyErr = errors.New("peer advanced")
	_, err := c.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "--this-install-only --version 2.1.0") || !strings.Contains(err.Error(), "--reconcile") {
		t.Fatal(err)
	}
}
