//go:build linux

package contract

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const managedUserUnit = "headroom.service"

func managedServiceSupervisor(environment []string, pid int) (string, error) {
	invocation := false
	execPID := ""
	for _, item := range environment {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if key == "INVOCATION_ID" {
			invocation = value != ""
		}
		if key == "SYSTEMD_EXEC_PID" {
			execPID = value
		}
	}
	if execPID != "" {
		advertised, err := strconv.Atoi(execPID)
		if err != nil || advertised != pid {
			// Desktop terminals and shells can inherit their parent's systemd
			// markers. They do not make ordinary child processes supervised.
			return "", nil
		}
		if err = validateManagedServiceSupervisor(managedSystemdUser, pid); err != nil {
			return "", errors.New("systemd-managed serve requires the fixed per-user headroom.service unit")
		}
		return managedSystemdUser, nil
	}
	if !invocation {
		return "", nil
	}
	// Older systemd versions may provide only INVOCATION_ID. Treat it as
	// supervised solely when the fixed unit independently reports this PID.
	mainPID, err := managedServiceSupervisorPID(managedSystemdUser)
	if err != nil || mainPID != pid {
		return "", nil
	}
	return managedSystemdUser, nil
}

func validateManagedServiceSupervisor(supervisor string, pid int) error {
	if supervisor == "" {
		return nil
	}
	if supervisor != managedSystemdUser || pid <= 0 {
		return errors.New("managed service supervisor is invalid")
	}
	output, err := runUserSystemctl("show", "--property", "MainPID", "--value", managedUserUnit)
	if err != nil {
		return err
	}
	got, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || got != pid {
		return errors.New("managed service supervisor process identity changed")
	}
	return nil
}

func stopManagedServiceSupervisor(supervisor string) error {
	if supervisor != managedSystemdUser {
		return errors.New("managed service supervisor is invalid")
	}
	_, err := runUserSystemctl("stop", managedUserUnit)
	return err
}

func startManagedServiceSupervisor(supervisor string) error {
	if supervisor != managedSystemdUser {
		return errors.New("managed service supervisor is invalid")
	}
	_, err := runUserSystemctl("start", managedUserUnit)
	return err
}

// supervisedManagedServiceFailure reports a failed or exited unit so a
// restart helper can stop waiting instead of timing out. It never reads
// journal contents; the caller points the operator at journalctl.
func supervisedManagedServiceFailure(supervisor string) (error, bool) {
	if supervisor != managedSystemdUser {
		return nil, false
	}
	output, err := runUserSystemctl("show", "--property", "ActiveState", "--property", "Result", "--property", "ExecMainStatus", managedUserUnit)
	if err != nil {
		return nil, false
	}
	fields := map[string]string{}
	var lines []string
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, line)
		if key, value, ok := strings.Cut(line, "="); ok {
			fields[key] = value
		}
	}
	active, result, status := fields["ActiveState"], fields["Result"], fields["ExecMainStatus"]
	if active == "" && len(lines) >= 3 {
		// Tolerate value-only output used by supervisors and fixtures.
		active, result, status = lines[0], lines[1], lines[2]
	}
	if active != "failed" && active != "inactive" {
		return nil, false
	}
	return fmt.Errorf("%s exited before reporting readiness (%s/%s); see journalctl --user -u %s", managedUserUnit, result, status, managedUserUnit), true
}

func managedServiceSupervisorPID(supervisor string) (int, error) {
	if supervisor != managedSystemdUser {
		return 0, errors.New("managed service supervisor is invalid")
	}
	output, err := runUserSystemctl("show", "--property", "MainPID", "--value", managedUserUnit)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || pid <= 0 {
		return 0, errors.New("managed service supervisor did not report a process")
	}
	return pid, nil
}

func runUserSystemctl(arguments ...string) ([]byte, error) {
	return userSystemctlCommand(arguments...)
}

var userSystemctlCommand = func(arguments ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/usr/bin/systemctl", append([]string{"--user"}, arguments...)...)
	runtimeDirectory := filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	info, statErr := os.Lstat(runtimeDirectory)
	stat, statOK := infoSysStat(info)
	busInfo, busErr := os.Lstat(filepath.Join(runtimeDirectory, "bus"))
	busStat, busStatOK := infoSysStat(busInfo)
	if statErr != nil || !statOK || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || int(stat.Uid) != os.Getuid() ||
		busErr != nil || !busStatOK || busInfo.Mode()&os.ModeSocket == 0 || busInfo.Mode()&os.ModeSymlink != 0 || int(busStat.Uid) != os.Getuid() {
		return nil, errors.New("the per-user systemd runtime is unavailable or unsafe")
	}
	command.Env = make([]string, 0, len(os.Environ())+2)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if key != "XDG_RUNTIME_DIR" && key != "DBUS_SESSION_BUS_ADDRESS" {
			command.Env = append(command.Env, item)
		}
	}
	command.Env = append(command.Env, "XDG_RUNTIME_DIR="+runtimeDirectory, "DBUS_SESSION_BUS_ADDRESS=unix:path="+filepath.Join(runtimeDirectory, "bus"))
	output := &messageLimitWriter{remaining: 4096}
	command.Stdout = output
	command.Stderr = &messageLimitWriter{remaining: 4096}
	if err := command.Run(); err != nil {
		return nil, errors.New("the per-user headroom.service supervisor command failed")
	}
	return output.Bytes(), nil
}

func infoSysStat(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat, ok
}

type messageLimitWriter struct {
	bytes.Buffer
	remaining int
}

func (w *messageLimitWriter) Write(value []byte) (int, error) {
	if len(value) > w.remaining {
		return 0, errors.New("supervisor output exceeded its bound")
	}
	w.remaining -= len(value)
	return w.Buffer.Write(value)
}
