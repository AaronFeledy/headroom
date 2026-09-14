//go:build darwin

package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

func showLaunchError(string) {}

func startApplication(executable string, arguments, environment []string, gui bool) error {
	if !macOSLaunchUsesAppKit(gui, environment) {
		return syscall.Exec(executable, append([]string{executable}, arguments...), environment)
	}
	payload, err := macOSLaunchPayload(executable, arguments, environment)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/usr/bin/osascript", "-l", "JavaScript", "-e", macOSLaunchScript)
	command.Env = environment
	command.Stdin = bytes.NewReader(payload)
	if err := command.Run(); err != nil {
		// Do not echo framework output: the launch configuration may contain
		// private environment values.
		return fmt.Errorf("launch Headroom with macOS Launch Services: %w", err)
	}
	return nil
}
