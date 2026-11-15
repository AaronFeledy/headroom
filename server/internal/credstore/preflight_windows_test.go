package credstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func Test_preflightReplaceable_checks_delete_access_and_keeps_sharing_errors_transient(t *testing.T) {
	for _, tc := range []struct {
		name     string
		probeErr error
		refused  bool
	}{
		{name: "writable"},
		{name: "access denied", probeErr: windows.ERROR_ACCESS_DENIED, refused: true},
		{name: "write protected", probeErr: windows.ERROR_WRITE_PROTECT, refused: true},
		{name: "sharing violation", probeErr: windows.ERROR_SHARING_VIOLATION},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "credentials.json")
			if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			originalProbe, originalClose := probeDeleteAccess, closeDeleteProbe
			t.Cleanup(func() { probeDeleteAccess, closeDeleteProbe = originalProbe, originalClose })
			probeDeleteAccess = func(_ *uint16, access, sharing uint32, _ *windows.SecurityAttributes, creation, flags uint32, _ windows.Handle) (windows.Handle, error) {
				if access != windows.DELETE || sharing != windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE || creation != windows.OPEN_EXISTING || flags != windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT {
					t.Error("credential probe did not request non-exclusive delete access without following reparse points")
				}
				return windows.Handle(42), tc.probeErr
			}
			closed := false
			closeDeleteProbe = func(windows.Handle) error { closed = true; return nil }
			err := preflightReplaceable(path)
			if errors.Is(err, ErrNotWritable) != tc.refused || (tc.probeErr != nil && !errors.Is(err, tc.probeErr)) || (tc.probeErr == nil && err != nil) {
				t.Fatalf("preflight = %v, probe error = %v, refused = %v", err, tc.probeErr, tc.refused)
			}
			if closed != (tc.probeErr == nil) {
				t.Fatalf("probe closed = %v, probe error = %v", closed, tc.probeErr)
			}
		})
	}
}
