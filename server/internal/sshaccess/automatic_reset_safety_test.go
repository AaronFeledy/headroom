//go:build linux

package sshaccess

import "testing"

func Test_ClaudeResetEndpoint_automatic_sshForward_intentionallyNotExecuted(t *testing.T) {
	t.Skip("INTENTIONALLY SKIPPED: forwarding the automatic reset routes can burn a very valuable banked reset; this is not a TODO")
}
