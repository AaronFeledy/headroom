package accounts

import (
	"fmt"

	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/contract"
)

type restartOutcomeKind uint8

const (
	restarted restartOutcomeKind = iota + 1
	manualRestart
	foregroundServe
	otherConfig
)

func renderRestart(outcome restartOutcomeKind, record *contract.ManagedServiceRecord, path string) string {
	switch outcome {
	case restarted:
		return "Restarted headroom.service."
	case manualRestart:
		return "To load the change, quit and reopen Headroom if its desktop app started the server. Otherwise restart the server the way you started it, or start one with: headroom serve"
	case foregroundServe:
		return "Stop the running headroom serve (Ctrl+C in its terminal) and start it again with the same options to load the change."
	case otherConfig:
		return fmt.Sprintf("The managed headroom serve reads %s, not %s, so it was not restarted. Restart the server that reads %s to load the change.", record.ConfigPath, path, path)
	}
	return ""
}

type restartErrorKind uint8

const (
	rolledBack restartErrorKind = iota + 1
	undoneNotRestarted
	notUndone
	notRestarted
)

type restartError struct {
	kind  restartErrorKind
	Path  string
	cause error
}

func (e *restartError) Error() string {
	switch e.kind {
	case rolledBack:
		return "headroom.service did not start with the change, so the change was undone and the service was restarted. Check: journalctl --user -u headroom.service"
	case undoneNotRestarted:
		return fmt.Sprintf("headroom.service did not start with the change. The change was undone, but the service did not restart: %v. Check: systemctl --user status headroom.service", e.cause)
	case notUndone:
		return fmt.Sprintf("headroom.service did not start with the change, and the change could not be undone: %v. Check: systemctl --user status headroom.service", e.cause)
	case notRestarted:
		return fmt.Sprintf("the change was saved to %s, but headroom.service was not restarted: %v. Restart it with: systemctl --user restart headroom.service", e.Path, e.cause)
	}
	return e.cause.Error()
}

func (e *restartError) Unwrap() error { return e.cause }
