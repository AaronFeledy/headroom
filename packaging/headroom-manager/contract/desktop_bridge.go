package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type desktopInfo struct {
	Schema       int    `json:"schema"`
	Product      string `json:"product"`
	OK           bool   `json:"ok"`
	Version      string `json:"version"`
	PID          int    `json:"pid"`
	Executable   string `json:"executable"`
	InstallRoot  string `json:"install_root"`
	RequestNonce string `json:"request_nonce"`
	Ready        *bool  `json:"ready"`
	Busy         *bool  `json:"busy"`
}

type bridgeBuffer struct{ bytes.Buffer }

func (b *bridgeBuffer) Write(data []byte) (int, error) {
	if b.Len()+len(data) > 16384 {
		return 0, errors.New("desktop bridge reply exceeds limit")
	}
	return b.Buffer.Write(data)
}

func callDesktop(ctx context.Context, executable string, request []byte) ([]byte, bool, error) {
	command := exec.CommandContext(ctx, executable, "--headroom-cli-request")
	command.WaitDelay = time.Second
	command.Stdin = bytes.NewReader(request)
	var output bridgeBuffer
	command.Stdout, command.Stderr = &output, io.Discard
	err := command.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 3 {
		return nil, true, nil
	}
	if err != nil {
		return output.Bytes(), false, fmt.Errorf("desktop bridge failed: %w", err)
	}
	return output.Bytes(), false, nil
}

func probeDesktop(ctx context.Context, executable, root string) (desktopInfo, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, activationTimeout)
	defer cancel()
	nonce, err := randomHex(24)
	if err != nil {
		return desktopInfo{}, false, err
	}
	payload, err := json.Marshal(struct {
		Schema       int    `json:"schema"`
		Product      string `json:"product"`
		Command      string `json:"command"`
		RequestNonce string `json:"request_nonce"`
		InstallRoot  string `json:"install_root"`
	}{SchemaVersion, "Headroom", "info", nonce, root})
	if err != nil {
		return desktopInfo{}, false, err
	}
	data, absent, err := callDesktop(ctx, executable, payload)
	if err != nil || absent {
		return desktopInfo{}, absent, err
	}
	var info desktopInfo
	if err = decodeBridgeReply(data, &info); err != nil {
		return info, false, err
	}
	if !info.OK || info.Schema != SchemaVersion || info.Product != "Headroom" || info.RequestNonce != nonce || info.Ready == nil || info.Busy == nil || !samePath(info.InstallRoot, root) || info.PID <= 0 {
		return info, false, errors.New("desktop info identity or readiness is invalid")
	}
	if err = verifyDesktopIdentity(root, info); err != nil {
		return info, false, err
	}
	return info, false, nil
}

func decodeBridgeReply(data []byte, value interface{}) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	// Info is additive; accepted reinstall uses the strict decoder below.
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("invalid desktop reply: %w", err)
	}
	return ensureEOF(decoder)
}

func verifyDesktopIdentity(root string, info desktopInfo) error {
	if !filepath.IsAbs(info.Executable) {
		return errors.New("desktop executable is not absolute")
	}
	relative, err := filepath.Rel(root, info.Executable)
	if err != nil {
		return err
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) < 3 || parts[0] != "versions" {
		return errors.New("desktop is outside the managed generations")
	}
	versionPath := strings.Join(parts[:2], "/")
	manifestPath := filepath.Join(root, filepath.FromSlash(versionPath), PackageManifestName)
	if err = validateInstallTargets(manifestPath, ""); err != nil {
		return err
	}
	data, err := readBoundedFile(manifestPath, maxManifestBytes)
	if err != nil {
		return err
	}
	manifest, err := DecodePackageManifest(bytes.NewReader(data))
	if err != nil {
		return err
	}
	hash, err := digestFile(manifestPath)
	if err != nil {
		return err
	}
	platform, arch, err := NativeTarget()
	if err != nil {
		return err
	}
	state := InstallState{Schema: SchemaVersion, Product: "Headroom", ActiveVersion: manifest.Version, VersionPath: versionPath,
		ManifestSHA256: hash, Platform: manifest.Platform, Architecture: manifest.Architecture, PackageAsset: manifest.AssetName}
	if manifest.PackageKind != "" || manifest.Platform != platform || manifest.Architecture != arch || info.Version != manifest.Version ||
		!samePath(info.Executable, installedComponentPath(root, versionPath, manifest.Components.Application.Path)) {
		return errors.New("desktop generation identity is invalid")
	}
	inspection, err := inspectState(root, state)
	if err != nil || !inspection.TrustedIdentity {
		return errors.New("desktop generation is not trusted")
	}
	// A damaged current generation can still be identified by its manifest and
	// native process. It cannot be used as the executable for bridge requests.
	token, err := captureProcessToken(info.PID, info.Executable)
	if err != nil {
		return err
	}
	watch, err := watchProcess(info.PID, info.Executable, token)
	if err != nil {
		return err
	}
	defer watch.Close()
	if processGone(info.PID) {
		return errors.New("desktop process exited")
	}
	return nil
}

func observePause(ctx context.Context) error {
	timer := time.NewTimer(25 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
