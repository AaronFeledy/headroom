package contract

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPublicInstallerActivation(t *testing.T) {
	switch runtime.GOOS {
	case "linux":
	case "windows":
		if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" {
			t.Skip("public Windows installation changes HKCU PATH; requires a disposable GitHub-hosted runner")
		}
	default:
		t.Skip("native public installer fixture supports Linux and Windows")
	}
	archive := activationArchive(t, "45.0.0", "")
	cliArchive := activationArchive(t, "45.0.0", PackageKindCLI)
	for _, test := range []struct {
		name, kind, archive, expected string
		noLaunch                      bool
	}{
		{"fresh ready", "", archive, "is running", false},
		{"no launch", "", archive, "Launch skipped", true},
		{"cli", PackageKindCLI, cliArchive, "for usage", false},
		{"coordinated ready", "", archive, "is running", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			home, data := filepath.Join(directory, "home with spaces"), filepath.Join(directory, "data with spaces")
			if err := os.MkdirAll(home, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Setenv("XDG_DATA_HOME", data)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(directory, "config"))
			t.Setenv("LOCALAPPDATA", filepath.Join(directory, "local app data"))
			t.Setenv("APPDATA", filepath.Join(directory, "roaming app data"))
			root := filepath.Join(directory, "install with spaces")
			entry := filepath.Join(home, ".local", "bin", "headroom-gui")
			cliEntry := filepath.Join(home, ".local", "bin", "headroom")
			if runtime.GOOS == "windows" {
				entry = filepath.Join(root, "headroom.exe")
				cliEntry = filepath.Join(root, "cli", "headroom.exe")
			}
			log := filepath.Join(directory, "launches.jsonl")
			t.Setenv("HEADROOM_FIXTURE_LAUNCH_LOG", log)
			if test.kind == "" {
				required := []string{
					filepath.Join(data, "applications", "headroom.desktop"),
					filepath.Join(data, "icons", "hicolor", "scalable", "apps", "headroom.svg"),
				}
				if runtime.GOOS == "windows" {
					required = []string{filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Headroom.lnk")}
				}
				t.Setenv("HEADROOM_FIXTURE_REQUIRE_FILES", strings.Join(required, string(os.PathListSeparator)))
			}
			t.Cleanup(func() { stopActivationFixture(t, root) })
			catalog, _ := (&updateFixture{t: t, version: "45.0.0", archive: test.archive, kind: test.kind}).release()
			releasePath := filepath.Join(directory, "release.json")
			if err := WriteJSON(releasePath, catalog); err != nil {
				t.Fatal(err)
			}
			args := []string{filepath.Join("..", "..", "..", "install.sh"), "--package", test.archive, "--release-manifest", releasePath,
				"--install-root", root, "--entry-path", entry, "--cli-entry-path", cliEntry}
			shell, noLaunchFlag, cliFlag := "sh", "--no-launch", "--cli"
			expected := test.expected
			if runtime.GOOS == "windows" {
				script, err := filepath.Abs(filepath.Join("..", "..", "..", "install.ps1"))
				if err != nil {
					t.Fatal(err)
				}
				shell, noLaunchFlag, cliFlag = "powershell.exe", "-NoLaunch", "-CLI"
				args = []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script,
					"-PackagePath", test.archive, "-ReleaseManifestPath", releasePath,
					"-InstallRoot", root, "-EntryPath", entry, "-CLIEntryPath", cliEntry}
				if test.kind == PackageKindCLI {
					expected = "headroom serve"
				}
			}
			if test.noLaunch {
				args = append(args, noLaunchFlag)
			}
			if test.kind == PackageKindCLI {
				args = append(args, cliFlag)
			}
			runInstaller := func() ([]byte, error) {
				command := exec.Command(shell, args...)
				if runtime.GOOS == "windows" {
					// Let Windows PowerShell rebuild its module path instead of inheriting pwsh's.
					for _, variable := range os.Environ() {
						name, _, _ := strings.Cut(variable, "=")
						if !strings.EqualFold(name, "PSModulePath") {
							command.Env = append(command.Env, variable)
						}
					}
				}
				return command.CombinedOutput()
			}
			runs := 1
			if test.name == "coordinated ready" {
				runs = 2
			}
			var generations, executables []string
			for i := 0; i < runs; i++ {
				output, err := runInstaller()
				if err != nil || !strings.Contains(string(output), expected) {
					t.Fatalf("public installer = %s, %v", output, err)
				}
				state, err := readTrustedState(root, true)
				if err != nil || state == nil || state.ActiveVersion != "45.0.0" {
					t.Fatalf("installed state = %+v, %v", state, err)
				}
				generations = append(generations, state.VersionPath)
				executable, _, err := ActiveExecutable(root)
				if err != nil {
					t.Fatal(err)
				}
				executables = append(executables, executable)
			}
			if test.name == "coordinated ready" {
				if generations[0] == generations[1] {
					t.Fatal("same-version reinstall reused the prior generation")
				}
				assertPublicInstallerApply(t, root, generations[1])
				t.Setenv("HEADROOM_FIXTURE_BUSY", "1")
				before, err := os.ReadFile(filepath.Join(root, StateName))
				if err != nil {
					t.Fatal(err)
				}
				output, err := runInstaller()
				if err == nil || strings.Contains(string(output), "Installed Headroom") {
					t.Fatalf("failed reinstall announced success: %s, %v", output, err)
				}
				after, err := os.ReadFile(filepath.Join(root, StateName))
				if err != nil || string(before) != string(after) {
					t.Fatal("busy installer changed state")
				}
			}
			want := runs
			if test.noLaunch || test.kind == PackageKindCLI {
				want = 0
			}
			content, err := os.ReadFile(log)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			launches := strings.FieldsFunc(string(content), func(r rune) bool { return r == '\n' })
			if len(launches) != want {
				t.Fatalf("desktop launches = %d, want %d", len(launches), want)
			}
			for i, launch := range launches {
				var primary desktopInfo
				if err := json.Unmarshal([]byte(launch), &primary); err != nil || !samePath(primary.Executable, executables[i]) {
					t.Fatalf("launch %d did not run the selected generation: %s, %v", i, launch, err)
				}
			}
		})
	}
}

func assertPublicInstallerApply(t *testing.T, root, generation string) {
	t.Helper()
	requests, err := filepath.Glob(filepath.Join(root, "transactions", "apply-*", TransactionRequestName))
	if err != nil || len(requests) != 1 {
		t.Fatalf("reinstall transactions = %v, %v", requests, err)
	}
	data, err := os.ReadFile(requests[0])
	var request ApplyRequest
	if err != nil || json.Unmarshal(data, &request) != nil || len(request.Nonce) != 48 {
		t.Fatalf("apply request = %s, %v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(root, "last-apply-result.json"))
	var outcome ApplyResult
	if err != nil || json.Unmarshal(data, &outcome) != nil || outcome.Status != "applied" || outcome.Nonce != request.Nonce || outcome.VersionPath != generation {
		t.Fatalf("uncorrelated reinstall outcome = %s, %v", data, err)
	}
}
