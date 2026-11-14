package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/contract"
)

func TestInstallActivationCommandJSONPreservesKnownResultOnError(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	previous := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = previous }()
	status := emit("install", contract.InstallResult{State: &contract.InstallState{ActiveVersion: "48.0.0", VersionPath: "versions/48.0.0"}}, errors.New("reinstall refused"))
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		OK      bool   `json:"ok"`
		Command string `json:"command"`
		Result  struct {
			State      contract.InstallState `json:"state"`
			Activation json.RawMessage       `json:"activation"`
		} `json:"result"`
		Error string `json:"error"`
	}
	if err = json.Unmarshal(data, &reply); err != nil {
		t.Fatal(err)
	}
	if status != 2 || reply.OK || reply.Command != "install" || reply.Result.State.ActiveVersion != "48.0.0" ||
		string(reply.Result.Activation) != "null" || reply.Error != "reinstall refused" {
		t.Fatalf("failed install lost its known result: %s, exit %d", data, status)
	}
}
