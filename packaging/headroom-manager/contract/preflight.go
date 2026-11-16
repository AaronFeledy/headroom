package contract

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// configCheckMinimumVersion is the first shipped CLI that understands
	// serve --check-config. Local recovery can target older releases.
	configCheckMinimumVersion = "3.6.0"
	configCheckTimeout        = 30 * time.Second
	configCheckMessageLimit   = 300
	configCheckStderrLimit    = 64 << 10
)

// checkStagedServerConfig runs the staged candidate's own CLI against the
// configuration the running managed serve recorded. It must run before the
// transaction is prepared so a rejected configuration never stops a healthy
// service. The candidate gets the plain server path, not the managed restart
// path, so it only validates and exits.
func checkStagedServerConfig(stage StageResult, manifest PackageManifest, request ApplyRequest) error {
	comparison, err := CompareVersions(manifest.Version, configCheckMinimumVersion)
	if err != nil {
		return err
	}
	if comparison < 0 {
		return nil
	}
	executable := filepath.Join(stage.PackageRoot, filepath.FromSlash(PackageCLIPath(manifest.Platform, manifest.PackageKind)))
	ctx, cancel := context.WithTimeout(context.Background(), configCheckTimeout)
	defer cancel()
	arguments := append([]string{"serve", "--check-config"}, request.ManagedServiceArguments...)
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Dir = request.ManagedServiceWorkingDirectory
	command.Env = preflightEnvironment(request.ManagedServiceEnvironment)
	command.Stdout = nil
	stderr := &boundedBuffer{remaining: configCheckStderrLimit}
	command.Stderr = stderr
	if runErr := command.Run(); runErr != nil {
		detail := firstMessageLine(stderr.String())
		if detail == "" {
			detail = runErr.Error()
		}
		detail = redactMessage(detail, request.ManagedServiceEnvironment, os.Environ())
		return fmt.Errorf("Headroom %s rejects the current server configuration: %s", manifest.Version, boundedMessage(detail, configCheckMessageLimit))
	}
	return nil
}

// preflightEnvironment keeps the apply's managed service environment but drops
// every HEADROOM_* variable the manager sets, so the candidate cannot re-enter
// the managed or launcher-restart path and only runs serverapp.Run.
func preflightEnvironment(saved []string) []string {
	return authoritativeApplyEnvironment(authoritativeServiceEnvironment(os.Environ(), saved), nil)
}

func firstMessageLine(value string) string {
	for _, line := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

func redactMessage(value string, environments ...[]string) string {
	for _, environment := range environments {
		for _, item := range environment {
			key, secret, ok := strings.Cut(item, "=")
			if ok && strings.EqualFold(key, "USAGE_AUTH_TOKEN") && secret != "" {
				value = strings.ReplaceAll(value, secret, "<redacted>")
			}
		}
	}
	return value
}

func boundedMessage(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// boundedBuffer keeps the first bytes of a command's output without failing the
// command once the bound is reached.
type boundedBuffer struct {
	bytes.Buffer
	remaining int
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	if b.remaining > 0 {
		kept := value
		if len(kept) > b.remaining {
			kept = kept[:b.remaining]
		}
		_, _ = b.Buffer.Write(kept)
		b.remaining -= len(kept)
	}
	return len(value), nil
}
