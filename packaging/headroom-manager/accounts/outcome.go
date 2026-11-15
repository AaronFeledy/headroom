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
	desktopRestarted
	desktopOtherConfig
	desktopBusy
)

func renderRestart(outcome restartOutcomeKind, record *contract.ManagedServiceRecord, path string) string {
	switch outcome {
	case restarted:
		return "Restarted headroom.service."
	case desktopRestarted:
		return "Restarted the Headroom desktop's local server."
	case desktopOtherConfig:
		return fmt.Sprintf("The Headroom desktop's server reads another config file, so it was not restarted. Restart the server that reads %s to load the change.", path)
	case desktopBusy:
		return "Headroom is installing an update; run this command again after it finishes, or quit and reopen Headroom."
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
	kind    restartErrorKind
	Path    string
	cause   error
	desktop bool
}

func (e *restartError) Error() string {
	if e.desktop {
		switch e.kind {
		case rolledBack:
			return "The Headroom desktop's local server did not start with the change, so the change was undone and the server was restarted."
		case undoneNotRestarted:
			return fmt.Sprintf("The Headroom desktop's local server did not start with the change. The change was undone, but the server did not restart: %v. Quit and reopen Headroom.", e.cause)
		case notUndone:
			return fmt.Sprintf("The Headroom desktop's local server did not start with the change, and the change could not be undone: %v. Check %s and quit and reopen Headroom.", e.cause, e.Path)
		case notRestarted:
			return fmt.Sprintf("The change was saved to %s, but Headroom did not confirm that its local server restarted: %v. If the account's card does not update, quit and reopen Headroom.", e.Path, e.cause)
		}
	}
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
