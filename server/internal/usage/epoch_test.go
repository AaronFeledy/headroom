package usage

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func Test_CredentialEpoch_changes_when_process_restarts(t *testing.T) {
	if os.Getenv("HEADROOM_SYNTHETIC_EPOCH_CHILD") == "1" {
		fmt.Println(*CredentialEpoch("synthetic-provider", "synthetic-credential"))
		return
	}
	// Given
	current := CredentialEpoch("synthetic-provider", "synthetic-credential")
	command := exec.Command(os.Args[0], "-test.run=^Test_CredentialEpoch_changes_when_process_restarts$")
	command.Env = append(os.Environ(), "HEADROOM_SYNTHETIC_EPOCH_CHILD=1")
	// When
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	// Then
	fields := strings.Fields(string(output))
	if len(fields) == 0 || len(fields[0]) != 64 || fields[0] == *current {
		t.Fatal("epoch reused across process restart")
	}
}
