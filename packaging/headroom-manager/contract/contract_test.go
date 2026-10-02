package contract

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestAssetNamesAndStrictVersions(t *testing.T) {
	cases := map[string]string{"windows/x86_64": "Headroom-v2.3.4-windows-x64.zip", "windows/arm64": "Headroom-v2.3.4-windows-arm64.zip", "linux/x86_64": "Headroom-v2.3.4-linux-x86_64.tar.gz", "macos/x86_64": "Headroom-v2.3.4-macos-x86_64.tar.gz", "macos/arm64": "Headroom-v2.3.4-macos-arm64.tar.gz"}
	for target, want := range cases {
		parts := strings.Split(target, "/")
		got, err := AssetName("2.3.4", parts[0], parts[1])
		if err != nil || got != want {
			t.Fatalf("%s: got %q, %v", target, got, err)
		}
	}
	for _, bad := range []string{"v1.2.3", "01.2.3", "1.02.3", "1.2", "1.2.3-01", "1.2.3-", "1.2.3+", "1.2.3+bad..id"} {
		if validVersion(bad) {
			t.Errorf("accepted invalid version %q", bad)
		}
	}
}

func TestCanonicalPathsRejectPortableAliases(t *testing.T) {
	for _, bad := range []string{"../x", "a/../x", "/root", "C:/x", "a\\b", "a//b", "bundle/CON.txt", "bundle/name. ", "bundle/name.", "bundle/a\x00b"} {
		if canonicalRelative(bad) {
			t.Errorf("accepted unsafe path %q", bad)
		}
	}
}

func macOSManifestFixture() PackageManifest {
	version := "2.3.4"
	paths := []string{
		"bootstrap/headroom", "bootstrap/headroom-package",
		"bundle/Headroom.app/Contents/Frameworks/QtCore.framework/QtCore",
		"bundle/Headroom.app/Contents/Frameworks/QtCore.framework/Resources",
		"bundle/Headroom.app/Contents/Frameworks/QtCore.framework/Versions/A/QtCore",
		"bundle/Headroom.app/Contents/Frameworks/QtCore.framework/Versions/A/Resources/Info.plist",
		"bundle/Headroom.app/Contents/Frameworks/QtCore.framework/Versions/Current",
		"bundle/Headroom.app/Contents/Info.plist",
		"bundle/Headroom.app/Contents/MacOS/headroom", "bundle/Headroom.app/Contents/MacOS/usage-server",
		"bundle/Headroom.app/Contents/PlugIns/iconengines/libqsvgicon.dylib",
		"bundle/Headroom.app/Contents/PlugIns/imageformats/libqsvg.dylib",
		"bundle/Headroom.app/Contents/PlugIns/platforms/libqcocoa.dylib",
		"bundle/Headroom.app/Contents/PlugIns/platforms/libqoffscreen.dylib",
		"bundle/Headroom.app/Contents/PlugIns/tls/libqsecuretransportbackend.dylib",
		"bundle/Headroom.app/Contents/Resources/headroom.icns",
		"bundle/Headroom.app/Contents/Resources/qml/QtQuick/Controls/Basic/qmldir",
		"bundle/bin/headroom-package", "bundle/share/headroom/THIRD_PARTY_NOTICES.txt",
		"bundle/share/licenses/headroom/LICENSE", "bundle/share/licenses/qt/attributions/index.json",
	}
	links := map[string]string{
		"bundle/Headroom.app/Contents/Frameworks/QtCore.framework/QtCore":           "Versions/Current/QtCore",
		"bundle/Headroom.app/Contents/Frameworks/QtCore.framework/Resources":        "Versions/Current/Resources",
		"bundle/Headroom.app/Contents/Frameworks/QtCore.framework/Versions/Current": "A",
	}
	files := make([]File, 0, len(paths))
	for _, name := range paths {
		record := File{Path: name, Size: 1, SHA256: strings.Repeat("a", 64), Mode: "0644"}
		if target := links[name]; target != "" {
			record.Size, record.SHA256, record.Mode, record.LinkTarget = int64(len(target)), hashString(target), "", target
		}
		if target := links[name]; target == "" && (name == "bootstrap/headroom" || name == "bootstrap/headroom-package" || name == "bundle/Headroom.app/Contents/MacOS/headroom" || name == "bundle/Headroom.app/Contents/MacOS/usage-server" || name == "bundle/bin/headroom-package" || strings.HasSuffix(name, "/QtCore") || strings.HasSuffix(name, ".dylib")) {
			record.Mode = "0755"
		}
		files = append(files, record)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	manifest := PackageManifest{Schema: SchemaVersion, Product: "Headroom", Version: version, Platform: "macos", Architecture: "arm64", AssetName: "Headroom-v2.3.4-macos-arm64.tar.gz", QtVersion: "6.12.0", Baseline: "macOS 14.4",
		Components: Components{Application: Component{packageApplicationPath("macos"), version}, Server: Component{packageServerPath("macos"), version}, Launcher: Component{"bootstrap/headroom", version}, Manager: Component{"bootstrap/headroom-package", version}}, Files: files}
	return manifest
}

func TestMacOSManifestRequiresNativeBundleAndScopedFrameworkLinks(t *testing.T) {
	manifest := macOSManifestFixture()
	if err := ValidateManifest(manifest); err != nil {
		t.Fatalf("valid macOS manifest rejected: %v", err)
	}
	broken := manifest
	broken.Files = append([]File(nil), manifest.Files...)
	for index := range broken.Files {
		if broken.Files[index].LinkTarget != "" {
			broken.Files[index].LinkTarget = "../../../../outside"
			break
		}
	}
	if err := ValidateManifest(broken); err == nil {
		t.Fatal("escaping framework link accepted")
	}
}

func TestInstallFreshUpgradeAndAuxiliaryRepair(t *testing.T) {
	root := t.TempDir()
	archive1 := makePackage(t, root, "1.2.3", false)
	installRoot := filepath.Join(root, "install with spaces")
	entry := filepath.Join(root, "bin with spaces", "headroom")
	if runtime.GOOS == "windows" {
		entry += ".exe"
	}
	platform, architecture, _ := NativeTarget()
	state, err := InstallArchive(archive1, installRoot, entry, Expectations{Version: "1.2.3", Platform: platform, Architecture: architecture})
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveVersion != "1.2.3" {
		t.Fatal(state)
	}
	if _, err = InstallArchive(archive1, installRoot, entry, Expectations{}); err != nil {
		t.Fatalf("matching-version repair failed: %v", err)
	}
	association, err := os.ReadFile(entry + ".root")
	if err != nil || string(association) != installRoot+"\n" {
		t.Fatalf("launcher association = %q, %v", association, err)
	}
	inspection := InspectInstall(installRoot)
	if !inspection.TrustedIdentity || !inspection.Complete {
		t.Fatalf("unexpected inspection: %+v", inspection)
	}
	expectedApplication := installedTestComponent(t, installRoot, inspection.VersionPath, false)
	if inspection.ActiveExecutable != expectedApplication {
		t.Fatalf("active executable = %q, want %q", inspection.ActiveExecutable, expectedApplication)
	}
	archive2 := makePackage(t, root, "1.2.4", false)
	state, err = InstallArchive(archive2, installRoot, entry, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveVersion != "1.2.4" {
		t.Fatal(state)
	}
	inspection = InspectInstall(installRoot)
	server := installedTestComponent(t, installRoot, inspection.VersionPath, true)
	if err = os.Remove(server); err != nil {
		t.Fatal(err)
	}
	inspection = InspectInstall(installRoot)
	if !inspection.TrustedIdentity || inspection.Complete || len(inspection.Missing) == 0 {
		t.Fatalf("repair identity lost: %+v", inspection)
	}
	executable, _, err := ActiveExecutable(installRoot)
	if err != nil || filepath.Base(executable) != "headroom"+nativeExtension() {
		t.Fatalf("desktop should remain launchable: %q %v", executable, err)
	}
	runtimePath := "plugins/platforms/libqoffscreen.so"
	if runtime.GOOS == "windows" {
		runtimePath = "plugins/platforms/qoffscreen.dll"
	} else if runtime.GOOS == "darwin" {
		runtimePath = "Headroom.app/Contents/PlugIns/platforms/libqoffscreen.dylib"
	}
	runtimeFile := filepath.Join(installRoot, filepath.FromSlash(inspection.VersionPath), filepath.FromSlash(runtimePath))
	if err = os.WriteFile(runtimeFile, []byte("corrupt runtime"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err = ActiveExecutable(installRoot); err == nil {
		t.Fatal("corrupt required runtime was launchable")
	}
	if _, err = InstallArchive(archive2, installRoot, entry, Expectations{}); err != nil {
		t.Fatalf("matching-version runtime repair failed: %v", err)
	}
	inspection = InspectInstall(installRoot)
	stableManager := filepath.Join(installRoot, "headroom-package"+nativeExtension())
	managerBytes, err := os.ReadFile(stableManager)
	if err != nil {
		t.Fatal(err)
	}
	managerBytes[len(managerBytes)/2] ^= 0xff
	if err = os.WriteFile(stableManager, managerBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	inspection = InspectInstall(installRoot)
	if inspection.Complete || !containsPath(inspection.Missing, "bootstrap/headroom-package"+nativeExtension()) {
		t.Fatalf("stable manager corruption was not detected: %+v", inspection)
	}
	if _, _, err = ActiveExecutable(installRoot); err != nil {
		t.Fatalf("running desktop could not reach bootstrap repair: %v", err)
	}
	if _, err = InstallArchive(archive2, installRoot, entry, Expectations{}); err != nil {
		t.Fatalf("stable manager repair failed: %v", err)
	}
	inspection = InspectInstall(installRoot)
	app := installedTestComponent(t, installRoot, inspection.VersionPath, false)
	if err = os.WriteFile(app, []byte("corrupt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err = ActiveExecutable(installRoot); err == nil {
		t.Fatal("corrupt desktop was launchable")
	}
}

func TestInstallRefusesUnmanagedUsageServerCompatibilityPath(t *testing.T) {
	if runtimeWindows() {
		t.Skip("the Windows package has no usage-server compatibility entry")
	}
	root := t.TempDir()
	installRoot := filepath.Join(root, "install")
	entry := filepath.Join(root, "bin", "headroom-gui")
	cliEntry := filepath.Join(root, "bin", "headroom")
	if err := os.MkdirAll(filepath.Dir(cliEntry), 0o755); err != nil {
		t.Fatal(err)
	}
	serverEntry := filepath.Join(filepath.Dir(cliEntry), "usage-server")
	if err := os.WriteFile(serverEntry, []byte("unmanaged\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive := makePackage(t, root, "1.0.0", false)
	if _, err := InstallArchiveWithCLIEntry(archive, installRoot, entry, cliEntry, Expectations{}); err == nil || !strings.Contains(err.Error(), "not managed") {
		t.Fatalf("unmanaged compatibility entry result = %v", err)
	}
	if data, err := os.ReadFile(serverEntry); err != nil || string(data) != "unmanaged\n" {
		t.Fatalf("unmanaged compatibility entry changed: %q, %v", data, err)
	}
}

func TestSuccessfulExternalReinstallClearsStaleApplyOutcome(t *testing.T) {
	root := t.TempDir()
	installRoot := filepath.Join(root, "install")
	entry := filepath.Join(root, "entry", "headroom"+nativeExtension())
	first := makePackage(t, root, "1.3.0", false)
	state, err := InstallArchive(first, installRoot, entry, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	if err = writeDurableJSON(filepath.Join(installRoot, "last-apply-result.json"), ApplyResult{Schema: SchemaVersion,
		Product: "Headroom", Status: "rolled_back", Version: state.ActiveVersion, VersionPath: state.VersionPath,
		RolledBack: true, FailureReason: "old failure"}); err != nil {
		t.Fatal(err)
	}
	second := makePackage(t, root, "1.3.1", false)
	if _, err = InstallArchive(second, installRoot, entry, Expectations{}); err != nil {
		t.Fatal(err)
	}
	inspection := InspectInstall(installRoot)
	if inspection.Version != "1.3.1" || inspection.ApplyStatus != "" || !inspection.Complete {
		t.Fatalf("reinstall inherited stale outcome: %+v", inspection)
	}
}

func containsPath(paths []string, wanted string) bool {
	for _, path := range paths {
		if path == wanted {
			return true
		}
	}
	return false
}

func TestInstallStateCannotRedirectVersionPath(t *testing.T) {
	root := t.TempDir()
	archive := makePackage(t, root, "2.0.0", false)
	installRoot := filepath.Join(root, "install")
	if _, err := InstallArchive(archive, installRoot, filepath.Join(root, "headroom"+nativeExtension()), Expectations{}); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(installRoot, StateName)
	var state InstallState
	data, _ := os.ReadFile(statePath)
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	state.VersionPath = "versions/../foreign"
	if err := WriteJSON(statePath, state); err != nil {
		t.Fatal(err)
	}
	if InspectInstall(installRoot).TrustedIdentity {
		t.Fatal("noncanonical version path was trusted")
	}
}

func TestMaterializeLinksKeepsSonameAsRegularFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks are a Linux package concern")
	}
	root := t.TempDir()
	target := filepath.Join(root, "libthing.so.1.2")
	link := filepath.Join(root, "libthing.so.1")
	if err := os.WriteFile(target, []byte("library"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(target), link); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeLinks(root); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("SONAME was not materialized: %v %v", info, err)
	}
}

func TestMaterializeLinksPreservesFrameworkLinks(t *testing.T) {
	root := t.TempDir()
	framework := filepath.Join(root, "QtCore.framework")
	version := filepath.Join(framework, "Versions", "A")
	if err := os.MkdirAll(filepath.Join(version, "Resources"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(version, "QtCore"), []byte("framework binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(version, "Resources", "Info.plist"), []byte("plist"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("A", filepath.Join(framework, "Versions", "Current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("Versions", "Current", "QtCore"), filepath.Join(framework, "QtCore")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("Versions", "Current", "Resources"), filepath.Join(framework, "Resources")); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeLinks(root); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{filepath.Join(framework, "Versions", "Current"), filepath.Join(framework, "QtCore"), filepath.Join(framework, "Resources")} {
		info, err := os.Lstat(name)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s framework link was not preserved: %v, %v", name, info, err)
		}
	}
}

func TestMaterializeLinksRejectsEscapingDirectory(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeLinks(root); err == nil {
		t.Fatal("escaping directory link was accepted")
	}
}

func TestValidationFailureDoesNotChangeActiveVersion(t *testing.T) {
	root := t.TempDir()
	installRoot := filepath.Join(root, "install")
	first := makePackage(t, root, "3.0.0", false)
	if _, err := InstallArchive(first, installRoot, filepath.Join(root, "headroom"+nativeExtension()), Expectations{}); err != nil {
		t.Fatal(err)
	}
	broken := makePackage(t, root, "3.1.0", true)
	if _, err := InstallArchive(broken, installRoot, filepath.Join(root, "headroom"+nativeExtension()), Expectations{}); err == nil {
		t.Fatal("broken package installed")
	}
	if got := InspectInstall(installRoot).Version; got != "3.0.0" {
		t.Fatalf("active version changed to %q", got)
	}
}

func TestStageResultIsOwnedAndRecorded(t *testing.T) {
	root := t.TempDir()
	archive := makePackage(t, root, "3.2.1", false)
	installRoot := filepath.Join(root, "install")
	stage, err := StageArchive(archive, installRoot, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	stagingRoot := filepath.Join(installRoot, "staging") + string(os.PathSeparator)
	if !strings.HasPrefix(stage.PackageRoot, stagingRoot) || filepath.Base(stage.PackageRoot) == "" {
		t.Fatalf("stage escaped owned root: %+v", stage)
	}
	recordPath := filepath.Join(filepath.Dir(filepath.Dir(stage.PackageRoot)), "verified-stage.json")
	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var recorded StageResult
	if err = json.Unmarshal(data, &recorded); err != nil || recorded != stage {
		t.Fatalf("verified stage record mismatch: %+v %v", recorded, err)
	}
}

func TestArchiveRejectsLinksAndCaseCollisions(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("tar mutation fixture runs on Linux x86_64")
	}
	parent := t.TempDir()
	_ = makePackage(t, parent, "1.0.0", false)
	rootName, _ := ArchiveRoot("1.0.0", "linux", "x86_64")
	packageRoot := filepath.Join(parent, rootName)
	linkArchive := filepath.Join(parent, "link.tar.gz")
	writeMutatedTar(t, packageRoot, linkArchive, &tar.Header{Name: rootName + "/link", Typeflag: tar.TypeSymlink, Linkname: "/tmp/x"}, "")
	if _, _, err := InspectArchive(linkArchive); err == nil || !strings.Contains(err.Error(), "links") {
		t.Fatalf("link rejection missing: %v", err)
	}
	caseArchive := filepath.Join(parent, "case.tar.gz")
	source := filepath.Join(packageRoot, "bundle", "bin", "headroom")
	writeMutatedTar(t, packageRoot, caseArchive, &tar.Header{Name: rootName + "/BUNDLE/bin/headroom", Mode: 0755, Typeflag: tar.TypeReg}, source)
	if _, _, err := InspectArchive(caseArchive); err == nil || !strings.Contains(err.Error(), "case-colliding") {
		t.Fatalf("case collision rejection missing: %v", err)
	}
}

func TestMalformedAndOversizedManifest(t *testing.T) {
	valid := PackageManifest{}
	data, _ := json.Marshal(valid)
	data = append(data, []byte(" trailing")...)
	if _, err := DecodePackageManifest(strings.NewReader(string(data))); err == nil {
		t.Fatal("trailing content accepted")
	}
	if _, err := DecodePackageManifest(strings.NewReader(strings.Repeat(" ", maxManifestBytes+1))); err == nil {
		t.Fatal("oversized manifest accepted")
	}
}

func TestReleaseManifestRejectsInvalidArchiveSizes(t *testing.T) {
	release := validReleaseManifest(t, "5.6.7")
	for name, size := range map[string]int64{"zero": 0, "negative": -1, "oversized": MaxArchiveBytes + 1} {
		t.Run(name, func(t *testing.T) {
			changed := release
			changed.Packages = append([]ReleasePackage(nil), release.Packages...)
			changed.Packages[0].Size = size
			data, _ := json.Marshal(changed)
			if _, err := DecodeReleaseManifest(strings.NewReader(string(data))); err == nil {
				t.Fatalf("accepted archive size %d", size)
			}
		})
	}
	data, _ := json.Marshal(release)
	var raw map[string]any
	_ = json.Unmarshal(data, &raw)
	raw["packages"].([]any)[0].(map[string]any)["size"] = true
	data, _ = json.Marshal(raw)
	if _, err := DecodeReleaseManifest(strings.NewReader(string(data))); err == nil {
		t.Fatal("accepted boolean archive size")
	}
}

func TestReleaseManifestAcceptsLegacyThreeTargetSet(t *testing.T) {
	release := validReleaseManifest(t, "5.6.7")
	release.Packages = release.Packages[:3]
	data, _ := json.Marshal(release)
	if _, err := DecodeReleaseManifest(strings.NewReader(string(data))); err != nil {
		t.Fatalf("legacy release manifest rejected: %v", err)
	}
}

func TestInspectRejectsArchiveSizeBounds(t *testing.T) {
	for name, size := range map[string]int64{"empty": 0, "oversized": MaxArchiveBytes + 1} {
		t.Run(name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "Headroom-v1.0.0-windows-x64.zip")
			file, err := os.Create(archive)
			if err != nil {
				t.Fatal(err)
			}
			if err = file.Truncate(size); err != nil {
				file.Close()
				t.Skipf("filesystem cannot create sparse archive fixture: %v", err)
			}
			file.Close()
			if _, _, err = InspectArchive(archive); err == nil || !strings.Contains(err.Error(), "size") {
				t.Fatalf("archive size %d rejection = %v", size, err)
			}
		})
	}
}

func TestManifestRejectsMissingRuntimeCaseCollisionAndUnsafeMode(t *testing.T) {
	root := t.TempDir()
	archive := makePackage(t, root, "6.0.0", false)
	manifest, _, err := InspectArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	required := "bundle/qml/QtQuick/Controls/Basic/qmldir"
	if manifest.Platform == "macos" {
		required = "bundle/Headroom.app/Contents/Resources/qml/QtQuick/Controls/Basic/qmldir"
	}
	missing := manifest
	missing.Files = removeFileRecord(missing.Files, required)
	if err = ValidateManifest(missing); err == nil || !strings.Contains(err.Error(), "runtime file missing") {
		t.Fatalf("missing runtime rejection = %v", err)
	}
	manager := "bundle/bin/headroom-package"
	if manifest.Platform == "windows" {
		manager += ".exe"
	}
	missingManager := manifest
	missingManager.Files = removeFileRecord(manifest.Files, manager)
	if err = ValidateManifest(missingManager); err == nil || !strings.Contains(err.Error(), "runtime file missing") {
		t.Fatalf("missing deployed manager rejection = %v", err)
	}
	collision := manifest
	collision.Files = append([]File(nil), manifest.Files...)
	copyRecord := collision.Files[0]
	copyRecord.Path = strings.ToUpper(copyRecord.Path)
	collision.Files = append(collision.Files, copyRecord)
	// Sorting makes the fixture reach portable case-collision validation rather
	// than failing only because file records must be ordered.
	sort.Slice(collision.Files, func(i, j int) bool { return collision.Files[i].Path < collision.Files[j].Path })
	if err = ValidateManifest(collision); err == nil {
		t.Fatal("case-colliding manifest records accepted")
	}
	if manifest.Platform == "linux" {
		unsafe := manifest
		unsafe.Files = append([]File(nil), manifest.Files...)
		unsafe.Files[0].Mode = "0777"
		if err = ValidateManifest(unsafe); err == nil {
			t.Fatal("world-writable package mode accepted")
		}
	}
}

func TestVerifyArchiveHashesPayloadAndForeignExpectationDoesNotMutate(t *testing.T) {
	root := t.TempDir()
	corrupt := makePackage(t, root, "7.0.0", true)
	if _, _, err := VerifyArchive(corrupt, Expectations{}); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("corrupt archive verification = %v", err)
	}
	valid := makePackage(t, root, "7.0.1", false)
	platform, _, _ := NativeTarget()
	foreign := "windows"
	if platform == "windows" {
		foreign = "linux"
	}
	installRoot := filepath.Join(root, "foreign-install")
	if _, err := StageArchive(valid, installRoot, Expectations{Platform: foreign}); err == nil {
		t.Fatal("foreign platform expectation was accepted")
	}
	if _, err := os.Stat(installRoot); !os.IsNotExist(err) {
		t.Fatalf("foreign stage mutated install root: %v", err)
	}
}

func removeFileRecord(files []File, name string) []File {
	result := make([]File, 0, len(files)-1)
	for _, file := range files {
		if file.Path != name {
			result = append(result, file)
		}
	}
	return result
}

func validReleaseManifest(t *testing.T, version string) ReleaseManifest {
	t.Helper()
	manifest := ReleaseManifest{Schema: SchemaVersion, Product: "Headroom", Version: version}
	for _, target := range [][2]string{{"windows", "x86_64"}, {"windows", "arm64"}, {"linux", "x86_64"}, {"macos", "x86_64"}, {"macos", "arm64"}} {
		platform, arch := target[0], target[1]
		asset, _ := AssetName(version, platform, arch)
		root, _ := ArchiveRoot(version, platform, arch)
		ext := ""
		if platform == "windows" {
			ext = ".exe"
		}
		components := Components{
			Application: Component{Path: packageApplicationPath(platform), Version: version},
			Server:      Component{Path: packageServerPath(platform), Version: version},
			Launcher:    Component{Path: "bootstrap/headroom" + ext, Version: version},
			Manager:     Component{Path: "bootstrap/headroom-package" + ext, Version: version},
		}
		if platform == "windows" {
			components.CredentialHelper = &Component{Path: "bundle/bin/headroom-credential-helper.exe", Version: version}
		}
		manifest.Packages = append(manifest.Packages, ReleasePackage{Platform: platform, Architecture: arch, AssetName: asset, Size: 1, SHA256: strings.Repeat("a", 64), PackageManifestPath: root + "/" + PackageManifestName, Components: components})
	}
	return manifest
}

func makePackage(t *testing.T, parent, version string, corrupt bool) string {
	t.Helper()
	platform, arch, err := NativeTarget()
	if err != nil {
		t.Fatal(err)
	}
	asset, err := AssetName(version, platform, arch)
	if err != nil {
		t.Fatal(err)
	}
	rootName, err := ArchiveRoot(version, platform, arch)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, rootName)
	if err = os.MkdirAll(filepath.Join(root, "bundle", "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(root, "bundle", "share", "headroom"), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(root, "bundle", "share", "licenses", "headroom"), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(root, "bootstrap"), 0755); err != nil {
		t.Fatal(err)
	}
	ext := nativeExtension()
	names := []string{packageApplicationPath(platform), packageServerPath(platform), PackageCLIPath(platform, ""), "bundle/bin/headroom-cli-launcher" + ext,
		"bundle/bin/headroom-package" + ext, "bootstrap/headroom" + ext, "bootstrap/headroom-cli" + ext, "bootstrap/headroom-package" + ext}
	if platform == "windows" {
		names = append(names, "bundle/bin/headroom-credential-helper.exe")
	}
	for _, name := range names {
		copyFixture(t, fixtureExecutable, filepath.Join(root, filepath.FromSlash(name)))
	}
	runtimeFiles := []string{"bundle/qml/QtQuick/Controls/Basic/qmldir"}
	if platform == "windows" {
		runtimeFiles = append(runtimeFiles, "bundle/bin/msvcp140.dll", "bundle/bin/vcruntime140.dll", "bundle/plugins/platforms/qwindows.dll", "bundle/plugins/platforms/qoffscreen.dll", "bundle/plugins/tls/qschannelbackend.dll", "bundle/plugins/imageformats/qsvg.dll", "bundle/plugins/iconengines/qsvgicon.dll")
	} else if platform == "linux" {
		runtimeFiles = []string{"bundle/qml/QtQuick/Controls/Basic/qmldir", "bundle/plugins/platforms/libqxcb.so", "bundle/plugins/platforms/libqwayland-generic.so", "bundle/plugins/platforms/libqoffscreen.so", "bundle/plugins/tls/libqopensslbackend.so", "bundle/plugins/imageformats/libqsvg.so", "bundle/plugins/iconengines/libqsvgicon.so"}
	} else {
		runtimeFiles = []string{"bundle/Headroom.app/Contents/Info.plist", "bundle/Headroom.app/Contents/Resources/headroom.icns", "bundle/Headroom.app/Contents/Frameworks/QtCore.framework/Versions/A/QtCore", "bundle/Headroom.app/Contents/Frameworks/QtCore.framework/Versions/A/Resources/Info.plist", "bundle/Headroom.app/Contents/PlugIns/platforms/libqcocoa.dylib", "bundle/Headroom.app/Contents/PlugIns/platforms/libqoffscreen.dylib", "bundle/Headroom.app/Contents/PlugIns/tls/libqsecuretransportbackend.dylib", "bundle/Headroom.app/Contents/PlugIns/imageformats/libqsvg.dylib", "bundle/Headroom.app/Contents/PlugIns/iconengines/libqsvgicon.dylib", "bundle/Headroom.app/Contents/Resources/qml/QtQuick/Controls/Basic/qmldir"}
	}
	for _, name := range runtimeFiles {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(full, []byte("synthetic runtime\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if platform == "macos" {
		for _, record := range macOSManifestFixture().Files {
			if record.LinkTarget != "" {
				if err := os.Symlink(record.LinkTarget, filepath.Join(root, filepath.FromSlash(record.Path))); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	os.WriteFile(filepath.Join(root, "bundle", "share", "headroom", "THIRD_PARTY_NOTICES.txt"), []byte("synthetic notice\n"), 0644)
	os.WriteFile(filepath.Join(root, "bundle", "share", "licenses", "headroom", "LICENSE"), []byte("synthetic license\n"), 0644)
	os.MkdirAll(filepath.Join(root, "bundle", "share", "licenses", "qt", "attributions"), 0755)
	os.WriteFile(filepath.Join(root, "bundle", "share", "licenses", "qt", "attributions", "index.json"), []byte("{}\n"), 0644)
	manifest, err := BuildManifest(root, version, platform, arch, "6.12.0", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err = WriteJSON(filepath.Join(root, PackageManifestName), manifest); err != nil {
		t.Fatal(err)
	}
	if corrupt {
		if err = os.WriteFile(filepath.Join(root, "bundle", "share", "headroom", "THIRD_PARTY_NOTICES.txt"), []byte("corrupted notice\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(parent, asset)
	if err = writeArchiveWithCompression(root, archive, gzip.BestSpeed); err != nil {
		t.Fatal(err)
	}
	return archive
}

func nativeExtension() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func installedTestComponent(t *testing.T, root, versionPath string, server bool) string {
	t.Helper()
	file, err := os.Open(filepath.Join(root, filepath.FromSlash(versionPath), PackageManifestName))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodePackageManifest(file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	component := manifest.Components.Application.Path
	if server {
		component = manifest.Components.Server.Path
	}
	return installedComponentPath(root, versionPath, component)
}

func writeMutatedTar(t *testing.T, root, output string, extra *tar.Header, extraSource string) {
	t.Helper()
	file, err := os.Create(output)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	writer := tar.NewWriter(gz)
	base := filepath.Base(root)
	err = filepath.Walk(root, func(name string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, name)
		header.Name = filepath.ToSlash(filepath.Join(base, rel))
		if err = writer.WriteHeader(header); err != nil {
			return err
		}
		source, err := os.Open(name)
		if err != nil {
			return err
		}
		_, err = io.Copy(writer, source)
		source.Close()
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if extraSource != "" {
		info, err := os.Stat(extraSource)
		if err != nil {
			t.Fatal(err)
		}
		extra.Size = info.Size()
	}
	if err = writer.WriteHeader(extra); err != nil {
		t.Fatal(err)
	}
	if extraSource != "" {
		source, err := os.Open(extraSource)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(writer, source)
		source.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
}

func copyFixture(t *testing.T, source, destination string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err = dst.Close(); err != nil {
		t.Fatal(err)
	}
}
