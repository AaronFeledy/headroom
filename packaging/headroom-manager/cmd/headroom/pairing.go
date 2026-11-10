package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/contract"
	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/pairing"
)

func pairIdentity(context.Context) (pairing.Identity, error) {
	root, inspection, _, err := managedInstall()
	if err != nil {
		return pairing.Identity{}, err
	}
	manager, _, err := contract.ImmutableManagerExecutable(root)
	if err != nil {
		return pairing.Identity{}, err
	}
	return pairing.Identity{Version: inspection.Version, Platform: inspection.Platform, Architecture: inspection.Architecture,
		Kind: inspection.PackageKind, Entry: inspection.CLIEntryPath, Root: root, Manager: manager, Trusted: inspection.TrustedIdentity && inspection.Complete}, nil
}

func nativePair(root string, config pairing.Config) *pairing.Native {
	return &pairing.Native{Root: root, Config: config, InspectFunc: pairIdentity, ApplyFunc: applyStagedUpdate, Client: contract.DefaultUpdateClient()}
}

type updateOptions struct {
	thisOnly  bool
	version   string
	reconcile bool
}

func parseUpdateOptions(args []string) (updateOptions, error) {
	var options updateOptions
	set := flag.NewFlagSet("update", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.BoolVar(&options.thisOnly, "this-install-only", false, "update only this installation")
	set.StringVar(&options.version, "version", "", "exact local recovery version")
	set.BoolVar(&options.reconcile, "reconcile", false, "reconcile an unfinished pair at the latest common release")
	if err := set.Parse(args); err != nil {
		return options, err
	}
	versionSet := false
	set.Visit(func(f *flag.Flag) {
		if f.Name == "version" {
			versionSet = true
		}
	})
	if versionSet && options.version == "" {
		return options, errors.New("--version requires a nonempty exact version")
	}
	if set.NArg() != 0 || options.reconcile && (options.thisOnly || options.version != "") || options.version != "" && !options.thisOnly {
		return options, errors.New("update accepts --this-install-only [--version VERSION], or --reconcile")
	}
	if options.version != "" {
		if _, err := contract.CompareVersions(options.version, options.version); err != nil {
			return options, err
		}
	}
	return options, nil
}

func coordinatedUpdate(ctx context.Context, args []string) error {
	options, err := parseUpdateOptions(args)
	if err != nil {
		return err
	}
	root, _, _, err := managedInstall()
	if err != nil {
		return err
	}
	config, err := pairing.LoadConfig(root)
	if options.thisOnly || errors.Is(err, os.ErrNotExist) && !options.reconcile {
		return updateLocal(ctx, options.version)
	}
	if err != nil {
		return err
	}
	lock, err := contract.LockPairedUpdate(root)
	if err != nil {
		return err
	}
	defer lock.Close()
	peer, err := pairing.NativeRunner(config)
	if err != nil {
		return err
	}
	coordinator := nativePair(root, config).Coordinator(peer, runtime.GOOS)
	coordinator.Reconcile = options.reconcile
	version, err := coordinator.Run(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("Headroom %s: paired update accepted.\n", version)
	return nil
}

func samePair(left, right pairing.Config) bool {
	left.State = pairing.StatePending
	right.State = pairing.StatePending
	return left == right
}

func pairCommand(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "windows-wsl" {
		return errors.New("usage: headroom pair windows-wsl --windows-entry PATH --wsl-distribution NAME --wsl-user USER --wsl-entry PATH")
	}
	set := flag.NewFlagSet("pair windows-wsl", flag.ContinueOnError)
	config := pairing.Config{Schema: 1, Product: "Headroom", State: pairing.StatePending}
	set.StringVar(&config.WindowsEntry, "windows-entry", "", "absolute native Windows public headroom.exe path")
	set.StringVar(&config.Distribution, "wsl-distribution", "", "exact local WSL distribution")
	set.StringVar(&config.User, "wsl-user", "", "exact WSL account")
	set.StringVar(&config.LinuxEntry, "wsl-entry", "", "absolute WSL public headroom path")
	if err := set.Parse(args[1:]); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return errors.New("pair has unexpected arguments")
	}
	root, _, _, err := managedInstall()
	if err != nil {
		return err
	}
	lock, err := contract.LockPairedUpdate(root)
	if err != nil {
		return err
	}
	defer lock.Close()
	config.ID, err = pairing.NewID()
	if err != nil {
		return err
	}
	if previous, loadErr := pairing.LoadConfig(root); loadErr == nil {
		config.ID = previous.ID
		if !samePair(config, previous) {
			return errors.New("this installation already has different Windows/WSL pairing settings")
		}
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		return loadErr
	}
	if err = config.Validate(); err != nil {
		return err
	}
	local, err := pairIdentity(ctx)
	if err != nil {
		return err
	}
	if err = config.ValidateIdentity(local, runtime.GOOS); err != nil {
		return err
	}
	peer, err := pairing.NativeRunner(config)
	if err != nil {
		return err
	}
	// The pending local record permits a nonce-bound reciprocal probe. An
	// interrupted handshake stays pending and cannot authorize any updates.
	if err = pairing.SaveConfig(root, config); err != nil {
		return err
	}
	response, err := peer.Call(ctx, pairing.Request{Command: "offer", Config: &config})
	if err != nil {
		return err
	}
	peerPlatform := "windows"
	if runtime.GOOS == "windows" {
		peerPlatform = "linux"
	}
	if response.Identity == nil || config.ValidateIdentity(*response.Identity, peerPlatform) != nil {
		return errors.New("pairing peer identity did not match")
	}
	config.State = pairing.StateActive
	if err = pairing.SaveConfig(root, config); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "Windows desktop and WSL server updates are now paired.")
	return nil
}

// pairRPC is a bounded local stdio protocol. It has no usage, provider, browser,
// or reset commands and must never grow a route that can consume a banked reset.
func pairRPC(ctx context.Context, input io.Reader, output io.Writer) error {
	var request pairing.Request
	if err := pairing.Decode(input, &request); err != nil {
		return err
	}
	if err := request.Validate(); err != nil {
		return err
	}
	response := pairing.Response{Schema: 1, Product: "Headroom", Nonce: request.Nonce, PairingID: request.PairingID}
	if err := handlePairRPC(ctx, request, &response); err != nil {
		response.OK = false
		response.Error = "paired_request_rejected"
		response.Identity = nil
		response.Prepared = nil
	} else {
		response.OK = true
	}
	return json.NewEncoder(output).Encode(response)
}

func handlePairRPC(ctx context.Context, request pairing.Request, response *pairing.Response) error {
	root, _, _, err := managedInstall()
	if err != nil {
		return err
	}
	local, err := pairIdentity(ctx)
	if err != nil {
		return err
	}
	stored, loadErr := pairing.LoadConfig(root)
	if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
		return loadErr
	}
	if request.Command == "offer" || request.Command == "probe" {
		config := *request.Config
		if err = config.ValidateIdentity(local, runtime.GOOS); err != nil {
			return err
		}
		peer, err := pairing.NativeRunner(config)
		if err != nil {
			return err
		}
		if loadErr == nil && !samePair(stored, config) {
			return errors.New("pairing identity changed")
		}
		if request.Command == "probe" {
			if loadErr != nil {
				return errors.New("pairing has no local pending offer")
			}
			response.Identity = &local
			return nil
		}
		lock, err := contract.LockPairedUpdate(root)
		if err != nil {
			return err
		}
		defer lock.Close()
		probe, err := peer.Call(ctx, pairing.Request{Command: "probe", Config: &config})
		if err != nil {
			return err
		}
		peerPlatform := "windows"
		if runtime.GOOS == "windows" {
			peerPlatform = "linux"
		}
		if probe.Identity == nil || config.ValidateIdentity(*probe.Identity, peerPlatform) != nil {
			return errors.New("reciprocal pairing probe failed")
		}
		config.State = pairing.StateActive
		if err = pairing.SaveConfig(root, config); err != nil {
			return err
		}
		response.Identity = &local
		return nil
	}
	if loadErr != nil || stored.State != pairing.StateActive || stored.ID != request.PairingID {
		return errors.New("pairing is not active")
	}
	if _, err = pairing.NativeRunner(stored); err != nil {
		return err
	}
	if err = stored.ValidateIdentity(local, runtime.GOOS); err != nil {
		return err
	}
	if request.Command == "inspect" {
		response.Identity = &local
		return nil
	}
	lock, err := contract.LockPairedUpdate(root)
	if err != nil {
		return err
	}
	defer lock.Close()
	native := nativePair(root, stored)
	switch request.Command {
	case "prepare":
		prepared, err := native.Prepare(ctx, request.Version)
		if err != nil {
			return err
		}
		response.Prepared = &prepared
		return nil
	case "commit":
		return native.Commit(ctx, pairing.Prepared{Version: request.Version, Operation: request.Operation})
	default:
		return errors.New("unsupported pairing operation")
	}
}
