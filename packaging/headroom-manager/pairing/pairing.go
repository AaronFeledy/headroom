// Package pairing coordinates explicitly associated Windows and WSL installs.
// It never infers ownership from a server URL and never sends provider data.
package pairing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/contract"
)

const RecordName = "pairing/windows-wsl.json"
const ProgressName = "pairing/update-progress.json"
const RPCArgument = "--headroom-pair-request"

type Config struct {
	Schema       int          `json:"schema"`
	Product      string       `json:"product"`
	ID           string       `json:"pairing_id"`
	State        pairingState `json:"state"`
	WindowsEntry string       `json:"windows_entry"`
	Distribution string       `json:"wsl_distribution"`
	User         string       `json:"wsl_user"`
	LinuxEntry   string       `json:"wsl_entry"`
}

func NewID() (string, error) {
	var value [24]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func validID(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 24 && strings.ToLower(value) == value
}

func safeText(value string, limit int) bool {
	return value != "" && len(value) <= limit && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0
}

func canonicalPath(value, platform string) bool {
	if !safeText(value, 4096) {
		return false
	}
	separator, rest := "/", value
	if platform == "windows" {
		if len(value) < 4 || !((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) || value[1:3] != ":\\" || strings.ContainsAny(value[2:], ":/*?\"<>|") {
			return false
		}
		separator, rest = "\\", value[3:]
	} else if platform == "linux" {
		if !strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
			return false
		}
		rest = value[1:]
	} else {
		return false
	}
	for _, segment := range strings.Split(rest, separator) {
		if segment == "" || segment == "." || segment == ".." || platform == "windows" && strings.TrimRight(segment, " .") != segment {
			return false
		}
	}
	return true
}

func (c Config) Validate() error {
	if c.Schema != 1 || c.Product != "Headroom" || !validID(c.ID) || (c.State != StatePending && c.State != StateActive) {
		return errors.New("invalid Windows/WSL pairing identity")
	}
	if !safeText(c.Distribution, 128) || strings.HasPrefix(c.Distribution, "-") || !regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.-]{0,127}\$?$`).MatchString(c.User) {
		return errors.New("an exact WSL distribution and user are required")
	}
	if !canonicalPath(c.WindowsEntry, "windows") || !strings.HasSuffix(strings.ToLower(c.WindowsEntry), "\\headroom.exe") {
		return errors.New("Windows entry must be an absolute native path ending in headroom.exe")
	}
	if !canonicalPath(c.LinuxEntry, "linux") || !strings.HasSuffix(c.LinuxEntry, "/headroom") {
		return errors.New("WSL entry must be an absolute Linux path ending in headroom")
	}
	return nil
}

type Identity struct {
	Version      string `json:"version"`
	Platform     string `json:"platform"`
	Architecture string `json:"architecture"`
	Kind         string `json:"package_kind"`
	Entry        string `json:"entry"`
	Root         string `json:"install_root"`
	Manager      string `json:"manager"`
	Trusted      bool   `json:"trusted"`
}

func (c Config) ValidateIdentity(identity Identity, platform string) error {
	entry := c.LinuxEntry
	if platform == "windows" {
		entry = c.WindowsEntry
	}
	sameEntry := entry == identity.Entry
	if platform == "windows" {
		sameEntry = strings.EqualFold(entry, identity.Entry)
	}
	if !identity.Trusted || identity.Platform != platform || !sameEntry ||
		(identity.Architecture != "x86_64" && identity.Architecture != "arm64") ||
		(platform == "windows" && identity.Kind != "") || (platform == "linux" && identity.Kind != contract.PackageKindCLI) ||
		!canonicalPath(identity.Root, platform) || !canonicalPath(identity.Manager, platform) || !canonicalPath(identity.Entry, platform) {
		return errors.New("paired installation identity does not match the selected entry")
	}
	if _, err := contract.CompareVersions(identity.Version, identity.Version); err != nil {
		return err
	}
	separator := "/"
	if platform == "windows" {
		separator = "\\"
	}
	root, manager := identity.Root, identity.Manager
	name := "headroom-package"
	if platform == "windows" {
		root = strings.ToLower(root)
		manager = strings.ToLower(manager)
		name += ".exe"
	}
	prefix := root + separator + "versions" + separator
	relative := strings.TrimPrefix(manager, prefix)
	parts := strings.Split(relative, separator)
	if !strings.HasPrefix(manager, prefix) || len(parts) != 3 || parts[1] != "bin" || parts[2] != name {
		return errors.New("paired package manager is not in its immutable installation")
	}
	return nil
}

type Prepared struct {
	Version   string `json:"version"`
	Operation string `json:"operation"`
	Current   bool   `json:"current"`
}

type Progress struct {
	Schema    int           `json:"schema"`
	Product   string        `json:"product"`
	PairingID string        `json:"pairing_id"`
	Version   string        `json:"version"`
	Phase     progressPhase `json:"phase"`
	Peer      *Identity     `json:"peer,omitempty"`
}

// Endpoint owns only its native installation. Commit acknowledges a durable
// native apply; Verify waits for its exact active generation and readiness.
type Endpoint interface {
	Inspect(context.Context) (Identity, error)
	Prepare(context.Context, string) (Prepared, error)
	Commit(context.Context, Prepared) error
	Verify(context.Context, string) error
}

type Coordinator struct {
	// Reconcile explicitly replaces an unfinished target only after inspecting
	// and verifying both current peers and staging a common non-downgrade target.
	Reconcile    bool
	Config       Config
	Platform     string
	Local        Endpoint
	Peer         Endpoint
	Latest       func(context.Context) (string, error)
	ReadProgress func() (Progress, error)
	SaveProgress func(Progress) error
	peerIdentity Identity
}

// Run stages both sides before applying either. Cross-kernel rollback is not
// atomic: after a peer succeeds, a durable receipt tells the next run to finish
// the exact remaining version. It never silently reports a split as success.
func (c Coordinator) Run(ctx context.Context) (string, error) {
	if c.Local == nil || c.Peer == nil || c.Latest == nil || c.SaveProgress == nil {
		return "", errors.New("paired update dependencies are incomplete")
	}
	if err := c.Config.Validate(); err != nil {
		return "", err
	}
	if c.Config.State != StateActive {
		return "", errors.New("Windows/WSL pairing has not completed")
	}
	peerPlatform := "windows"
	if c.Platform == "windows" {
		peerPlatform = "linux"
	} else if c.Platform != "linux" {
		return "", errors.New("pairing requires Windows or WSL")
	}
	local, err := c.Local.Inspect(ctx)
	if err != nil {
		return "", err
	}
	if err = c.Config.ValidateIdentity(local, c.Platform); err != nil {
		return "", err
	}
	var previous Progress
	if c.ReadProgress != nil {
		previous, err = c.ReadProgress()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	version := ""
	if previous.Phase != "" {
		if previous.Schema != 1 || previous.Product != "Headroom" || previous.PairingID != c.Config.ID {
			return "", errors.New("paired update recovery identity is invalid")
		}
		switch previous.Phase {
		case progressPhasePrepared, progressPhasePeerCommitting, progressPhasePeerApplied, progressPhaseComplete:
		default:
			return "", errors.New("paired update recovery phase is invalid")
		}
		if _, err = contract.CompareVersions(previous.Version, previous.Version); err != nil {
			return "", err
		}
	}
	if c.Reconcile && (previous.Phase == "" || previous.Phase == progressPhaseComplete) {
		return "", errors.New("there is no unfinished paired update to reconcile")
	}
	peerConfirmed := !c.Reconcile && (previous.Phase == progressPhasePeerCommitting || previous.Phase == progressPhasePeerApplied)
	var peer Identity
	if peerConfirmed {
		if previous.Peer == nil || c.Config.ValidateIdentity(*previous.Peer, peerPlatform) != nil {
			return "", errors.New("paired recovery probe identity is invalid")
		}
		peer = *previous.Peer
		c.peerIdentity = peer
		if restorer, ok := c.Peer.(interface{ RestoreProbe(Identity) error }); ok {
			if err = restorer.RestoreProbe(peer); err != nil {
				return "", err
			}
		}
		if err = c.Peer.Verify(ctx, previous.Version); err != nil {
			return "", fmt.Errorf("paired update outcome is unresolved. Run headroom update --this-install-only --version %s on the paired installation, then retry. If either side has advanced past that version, run headroom update --reconcile here: %w", previous.Version, err)
		}
		peer.Version = previous.Version
	} else {
		peer, err = c.Peer.Inspect(ctx)
		if err != nil {
			return "", fmt.Errorf("paired installation is unavailable: %w", err)
		}
		if err = c.Config.ValidateIdentity(peer, peerPlatform); err != nil {
			return "", err
		}
		c.peerIdentity = peer
	}
	if c.Reconcile {
		if err = c.Local.Verify(ctx, local.Version); err != nil {
			return "", fmt.Errorf("local installation is not ready for reconciliation: %w", err)
		}
		if err = c.Peer.Verify(ctx, peer.Version); err != nil {
			return "", fmt.Errorf("paired installation is not ready for reconciliation: %w", err)
		}
	}
	if !c.Reconcile && previous.Phase != "" && previous.Phase != progressPhaseComplete {
		if previous.Schema != 1 || previous.Product != "Headroom" || previous.PairingID != c.Config.ID {
			return "", errors.New("paired update recovery identity is invalid")
		}
		version = previous.Version
	} else {
		version, err = c.Latest(ctx)
		if err != nil {
			return "", err
		}
	}
	installedVersions := []string{local.Version, peer.Version}
	if c.Reconcile {
		installedVersions = append(installedVersions, previous.Version)
	}
	for _, installed := range installedVersions {
		comparison, compareErr := contract.CompareVersions(version, installed)
		if compareErr != nil || comparison < 0 {
			return "", errors.New("paired target is older than an installation or unfinished target; use headroom update --reconcile when a common newer release is available")
		}
	}
	if local.Version == version && peer.Version == version {
		if err = c.Local.Verify(ctx, version); err != nil {
			return "", err
		}
		if err = c.Peer.Verify(ctx, version); err != nil {
			return "", err
		}
		return version, c.save(version, progressPhaseComplete)
	}
	preparedLocal, err := c.Local.Prepare(ctx, version)
	if err != nil {
		return "", fmt.Errorf("local preparation failed; neither installation was changed: %w", err)
	}
	validPrepared := func(p Prepared, installed string) bool {
		return p.Version == version && ((!p.Current && validID(p.Operation)) || (p.Current && p.Operation == "" && installed == version))
	}
	if !validPrepared(preparedLocal, local.Version) {
		return "", errors.New("local preparation does not match the exact target")
	}
	preparedPeer := Prepared{Version: version, Current: true}
	if !peerConfirmed {
		preparedPeer, err = c.Peer.Prepare(ctx, version)
		if err != nil {
			return "", fmt.Errorf("paired preparation failed; neither installation was changed: %w", err)
		}
		if !validPrepared(preparedPeer, peer.Version) {
			return "", errors.New("paired preparation does not match the exact target")
		}
	}
	if err = c.save(version, progressPhasePrepared); err != nil {
		return "", err
	}
	if !preparedPeer.Current {
		if err = c.save(version, progressPhasePeerCommitting); err != nil {
			return "", err
		}
		commitErr := c.Peer.Commit(ctx, preparedPeer)
		// The command can lose its response while the native apply succeeds.
		// Verify exact installed readiness before deciding whether it completed.
		if err = c.Peer.Verify(ctx, version); err != nil {
			return "", fmt.Errorf("paired update did not confirm completion; local installation is unchanged. Run headroom update again to recover: %w", errors.Join(commitErr, err))
		}
	} else if err = c.Peer.Verify(ctx, version); err != nil {
		return "", err
	}
	if err = c.save(version, progressPhasePeerApplied); err != nil {
		return "", err
	}
	if !preparedLocal.Current {
		if err = c.Local.Commit(ctx, preparedLocal); err != nil {
			return "", fmt.Errorf("paired installation is at %s; local update still needs to finish. Run headroom update again: %w", version, err)
		}
		// The initiating process must now exit for its native transaction. Keep
		// peer_applied until the next invocation verifies the completed pair.
		return version, nil
	}
	if err = c.Local.Verify(ctx, version); err != nil {
		return "", err
	}
	return version, c.save(version, progressPhaseComplete)
}

func (c Coordinator) save(version string, phase progressPhase) error {
	if c.SaveProgress == nil {
		return errors.New("paired update has no durable recovery store")
	}
	return c.SaveProgress(Progress{Schema: 1, Product: "Headroom", PairingID: c.Config.ID, Version: version, Phase: phase, Peer: &c.peerIdentity})
}

func WaitVerified(ctx context.Context, version string, inspect func(context.Context) (Identity, error)) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	for {
		identity, err := inspect(ctx)
		if err == nil && identity.Trusted && identity.Version == version {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("the paired installation did not report the requested active version")
		case <-time.After(time.Second):
		}
	}
}
