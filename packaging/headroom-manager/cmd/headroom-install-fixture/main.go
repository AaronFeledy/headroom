// Command headroom-install-fixture is a disposable, provider-free package fixture.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/contract"
)

type primaryInfo struct {
	Schema       int    `json:"schema"`
	Product      string `json:"product"`
	OK           bool   `json:"ok"`
	Version      string `json:"version"`
	PID          int    `json:"pid"`
	Executable   string `json:"executable"`
	InstallRoot  string `json:"install_root"`
	RequestNonce string `json:"request_nonce"`
	Ready        bool   `json:"ready"`
	Busy         bool   `json:"busy"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "package" {
		if err := buildPackage(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "--headroom-cli-request" {
		os.Exit(bridge())
	}
	executable, err := os.Executable()
	if err != nil {
		os.Exit(2)
	}
	root := os.Getenv("HEADROOM_INSTALL_ROOT")
	for _, required := range filepath.SplitList(os.Getenv("HEADROOM_FIXTURE_REQUIRE_FILES")) {
		if _, err := os.Stat(required); err != nil {
			os.Exit(2)
		}
	}
	version, err := executableVersion(executable)
	if err != nil {
		os.Exit(2)
	}
	info := primaryInfo{Schema: 1, Product: "Headroom", OK: true, Version: version, PID: os.Getpid(), Executable: executable, InstallRoot: root, Ready: true,
		Busy: os.Getenv("HEADROOM_FIXTURE_BUSY") == "1"}
	primary := filepath.Join(root, "fixture-primary.json")
	if err = contract.WriteJSON(primary, info); err != nil {
		os.Exit(2)
	}
	defer os.Remove(primary)
	if log := os.Getenv("HEADROOM_FIXTURE_LAUNCH_LOG"); log != "" {
		file, openErr := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if openErr != nil {
			os.Exit(2)
		}
		if err = json.NewEncoder(file).Encode(info); err != nil {
			os.Exit(2)
		}
		if err = file.Close(); err != nil {
			os.Exit(2)
		}
	}
	readyPath := ""
	for i := 1; i+1 < len(os.Args); i++ {
		if os.Args[i] == "--headroom-ready-file" {
			readyPath = os.Args[i+1]
		}
	}
	if os.Getenv("HEADROOM_FIXTURE_CRASH_VERSION") == version {
		return
	}
	if readyPath != "" && os.Getenv("HEADROOM_FIXTURE_NO_READY_VERSION") != version && os.Getenv("HEADROOM_FIXTURE_READY_MODE") != "missing" {
		if os.Getenv("HEADROOM_FIXTURE_READY_MODE") == "delayed" {
			timer := time.NewTimer(150 * time.Millisecond)
			<-timer.C
		}
		marker := struct {
			Nonce      string `json:"nonce"`
			PID        int    `json:"pid"`
			Version    string `json:"version"`
			Executable string `json:"executable"`
		}{os.Getenv("HEADROOM_READY_NONCE"), os.Getpid(), version, executable}
		switch os.Getenv("HEADROOM_FIXTURE_READY_MODE") {
		case "wrong_nonce":
			marker.Nonce = "wrong"
		case "wrong_pid":
			marker.PID = os.Getppid()
		case "wrong_path":
			marker.Executable = os.Args[0] + "-other"
		case "wrong_version":
			marker.Version = "0.0.0"
		case "early_exit":
			return
		}
		if os.Getenv("HEADROOM_FIXTURE_READY_MODE") == "malformed" {
			if err = os.WriteFile(readyPath, []byte("{invalid"), 0o600); err != nil {
				os.Exit(2)
			}
		} else if err = contract.WriteJSON(readyPath, marker); err != nil {
			os.Exit(2)
		}
	}
	stop := filepath.Join(root, fmt.Sprintf("fixture-stop-%d", os.Getpid()))
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err := os.Stat(stop); err == nil {
				return
			}
		case <-deadline.C:
			return
		}
	}
}

func executableVersion(executable string) (string, error) {
	for directory := filepath.Dir(executable); directory != filepath.Dir(directory); directory = filepath.Dir(directory) {
		file, err := os.Open(filepath.Join(directory, contract.PackageManifestName))
		if err != nil {
			continue
		}
		manifest, decodeErr := contract.DecodePackageManifest(file)
		closeErr := file.Close()
		if decodeErr != nil || closeErr != nil {
			return "", errors.Join(decodeErr, closeErr)
		}
		return manifest.Version, nil
	}
	return "", errors.New("fixture executable has no package manifest")
}

func bridge() int {
	var request struct {
		Command             string                  `json:"command"`
		InstallRoot         string                  `json:"install_root"`
		RequestNonce        string                  `json:"request_nonce"`
		PriorStateSHA256    string                  `json:"prior_state_sha256"`
		PreparedStage       contract.StageResult    `json:"prepared_stage"`
		AdditionalProcesses []contract.ProcessClaim `json:"additional_processes"`
	}
	if json.NewDecoder(io.LimitReader(os.Stdin, 16384)).Decode(&request) != nil {
		return 2
	}
	root := request.InstallRoot
	if root == "" {
		root = os.Getenv("HEADROOM_INSTALL_ROOT")
	}
	data, err := os.ReadFile(filepath.Join(root, "fixture-primary.json"))
	if errors.Is(err, os.ErrNotExist) {
		return 3
	}
	var info primaryInfo
	if err != nil || json.Unmarshal(data, &info) != nil {
		return 2
	}
	info.RequestNonce = request.RequestNonce
	info.Busy = info.Busy || os.Getenv("HEADROOM_FIXTURE_BUSY") == "1"
	switch request.Command {
	case "info":
		if os.Getenv("HEADROOM_FIXTURE_OLD_BRIDGE") == "1" {
			info.RequestNonce = ""
		}
		if json.NewEncoder(os.Stdout).Encode(info) != nil {
			return 2
		}
		return 0
	case "reinstall":
		if info.Busy || request.AdditionalProcesses == nil || len(request.AdditionalProcesses) != 0 {
			return 2
		}
		requestLog, logErr := os.OpenFile(filepath.Join(root, "fixture-reinstall.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if logErr != nil {
			return 2
		}
		encodeErr := json.NewEncoder(requestLog).Encode(request)
		if closeErr := requestLog.Close(); encodeErr != nil || closeErr != nil {
			return 2
		}
		manager := filepath.Join(root, "headroom-package")
		if filepath.Ext(info.Executable) == ".exe" {
			manager += ".exe"
		}
		stateData, readErr := os.ReadFile(filepath.Join(root, contract.StateName))
		var state contract.InstallState
		if readErr != nil || json.Unmarshal(stateData, &state) != nil {
			return 2
		}
		record := filepath.Join(filepath.Dir(filepath.Dir(request.PreparedStage.PackageRoot)), "verified-stage.json")
		command := exec.Command(manager, "prepare-apply", "--install-root", root, "--entry-path", state.ApplicationEntryPath,
			"--stage-record", record, "--current-pid", fmt.Sprint(info.PID), "--current-executable", info.Executable,
			"--prior-state-sha256", request.PriorStateSHA256)
		preparedData, err := command.Output()
		var prepared struct {
			OK     bool                   `json:"ok"`
			Result contract.PreparedApply `json:"result"`
		}
		if err != nil || json.Unmarshal(preparedData, &prepared) != nil || !prepared.OK {
			return 2
		}
		if err = contract.CommitPreparedApply(prepared.Result); err != nil {
			return 2
		}
		if os.Getenv("HEADROOM_FIXTURE_LOST_REPLY") == "1" {
			return 2
		}
		if err = os.WriteFile(filepath.Join(root, fmt.Sprintf("fixture-stop-%d", info.PID)), nil, 0o600); err != nil {
			return 2
		}
		reply := struct {
			OK                   bool   `json:"ok"`
			Status               string `json:"status"`
			RequestNonce         string `json:"request_nonce"`
			Version              string `json:"version"`
			TransactionDirectory string `json:"transaction_directory"`
			Nonce                string `json:"nonce"`
		}{true, "accepted", request.RequestNonce, request.PreparedStage.Version, prepared.Result.TransactionDirectory, prepared.Result.Nonce}
		if json.NewEncoder(os.Stdout).Encode(reply) != nil {
			return 2
		}
		return 0
	default:
		return 2
	}
}
