package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/accounts"
	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/contract"
)

// Desktop restart waits on the owned server's bounded 60s readiness; keep a
// short margin so the CLI deadline is not the first to fire.
const desktopRestartTimeout = 75 * time.Second

type accountsDesktop struct{}

var _ accounts.Desktop = accountsDesktop{}

func (accountsDesktop) RestartServer(ctx context.Context, configPath string) (accounts.DesktopRestart, error) {
	root := os.Getenv("HEADROOM_INSTALL_ROOT")
	if root == "" {
		return accounts.DesktopUnavailable, nil
	}
	if contract.InspectInstall(root).PackageKind == contract.PackageKindCLI {
		return accounts.DesktopUnavailable, nil
	}
	application, _, err := contract.ActiveExecutableForRole(root, contract.RoleApplication)
	if err != nil {
		return accounts.DesktopUnavailable, nil
	}
	return callDesktopRestart(ctx, application, configPath)
}

func callDesktopRestart(ctx context.Context, application, configPath string) (accounts.DesktopRestart, error) {
	ctx, cancel := context.WithTimeout(ctx, desktopRestartTimeout)
	defer cancel()
	payload, err := json.Marshal(map[string]any{
		"schema": 1, "product": "Headroom", "command": "restart-server", "config_path": configPath,
	})
	if err != nil {
		return 0, err
	}
	command := exec.CommandContext(ctx, application, "--headroom-cli-request")
	command.Stdin = bytes.NewReader(payload)
	var output bytes.Buffer
	command.Stdout = &limitedWriter{writer: &output, remaining: 4096}
	command.Stderr = io.Discard
	runErr := command.Run()
	// A reply that arrived is authoritative even if ctx ended afterwards.
	if runErr != nil && ctx.Err() != nil {
		return 0, fmt.Errorf("%w: %w", accounts.ErrDesktopOutcomeUnknown, ctx.Err())
	}
	return mapDesktopRestartReply(output.Bytes(), runErr)
}

type desktopRestartReply struct {
	OK         bool   `json:"ok"`
	Result     string `json:"result"`
	Error      string `json:"error"`
	ConfigPath string `json:"config_path"`
}

func mapDesktopRestartReply(output []byte, runErr error) (accounts.DesktopRestart, error) {
	if runErr != nil {
		var exit *exec.ExitError
		if errors.As(runErr, &exit) && exit.ExitCode() == 3 {
			return accounts.DesktopUnavailable, nil
		}
		if errors.As(runErr, &exit) && exit.ExitCode() == 1 && len(bytes.TrimSpace(output)) == 0 {
			return accounts.DesktopUnavailable, nil
		}
		return 0, fmt.Errorf("desktop restart-server request failed: %w", runErr)
	}
	if len(bytes.TrimSpace(output)) == 0 {
		return accounts.DesktopUnavailable, nil
	}
	var reply desktopRestartReply
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	var trailing any
	if err := decoder.Decode(&reply); err != nil || !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return 0, errors.New("desktop returned an invalid restart-server reply")
	}
	if !reply.OK {
		message := strings.TrimSpace(reply.Error)
		if strings.EqualFold(message, "unknown command") {
			return accounts.DesktopUnavailable, nil
		}
		if message == "" {
			message = "desktop restart-server failed"
		}
		return 0, errors.New(message)
	}
	switch reply.Result {
	case "busy":
		return accounts.DesktopBusy, nil
	case "not_owner":
		return accounts.DesktopNotOwner, nil
	case "other_config":
		return accounts.DesktopOtherConfig, nil
	case "restarted":
		return accounts.DesktopRestarted, nil
	default:
		return accounts.DesktopUnavailable, nil
	}
}
