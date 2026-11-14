package contract

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func rejectPreparedReinstall(root string) error {
	entries, err := os.ReadDir(filepath.Join(root, "transactions"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "apply-") {
			continue
		}
		journal, err := readJournal(filepath.Join(root, "transactions", entry.Name(), TransactionJournalName))
		if err != nil {
			return err
		}
		if journal.Phase == journalPhasePrepared {
			return errors.New("another reinstall or update is being prepared")
		}
		if journal.Phase == journalPhaseWaitingForExit {
			token, tokenErr := captureProcessToken(journal.Request.CurrentPID, journal.Request.CurrentExecutable)
			if tokenErr == nil && token == journal.Request.CurrentProcessToken {
				return errors.New("another reinstall or update is waiting for the desktop")
			}
		}
	}
	return nil
}
