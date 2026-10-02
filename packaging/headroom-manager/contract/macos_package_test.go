package contract

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestMacOSFrameworkArchiveRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("framework symlink fixture runs on Unix")
	}
	fixture := macOSManifestFixture()
	root := filepath.Join(t.TempDir(), strings.TrimSuffix(fixture.AssetName, ".tar.gz"))
	// A minimal Mach-O header is enough for architecture validation. This
	// fixture is never executed and has no provider or network behavior.
	header := make([]byte, 32)
	binary.LittleEndian.PutUint32(header[0:], 0xfeedfacf)
	binary.LittleEndian.PutUint32(header[4:], 0x0100000c) // CPU_TYPE_ARM64
	binary.LittleEndian.PutUint32(header[12:], 2)         // MH_EXECUTE
	for _, record := range fixture.Files {
		name := filepath.Join(root, filepath.FromSlash(record.Path))
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if record.LinkTarget != "" {
			continue
		}
		mode := os.FileMode(0o644)
		if record.Mode == "0755" {
			mode = 0o755
		}
		if err := os.WriteFile(name, header, mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, record := range fixture.Files {
		if record.LinkTarget != "" {
			if err := os.Symlink(record.LinkTarget, filepath.Join(root, filepath.FromSlash(record.Path))); err != nil {
				t.Fatal(err)
			}
		}
	}
	manifest, err := BuildManifest(root, fixture.Version, "macos", "arm64", "6.12.0", "macOS 14.4")
	if err != nil {
		t.Fatal(err)
	}
	if err = WriteJSON(filepath.Join(root, PackageManifestName), manifest); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), manifest.AssetName)
	if err = WriteArchive(root, archive); err != nil {
		t.Fatal(err)
	}
	got, extracted, err := ExtractAndVerify(archive, filepath.Join(t.TempDir(), "extract"), Expectations{Platform: "macos", Architecture: "arm64"})
	if err != nil {
		t.Fatal(err)
	}
	copyRoot := t.TempDir()
	if err = copyTree(filepath.Join(extracted, "bundle"), filepath.Join(copyRoot, "bundle")); err != nil {
		t.Fatal(err)
	}
	if err = copyTree(filepath.Join(extracted, "bootstrap"), filepath.Join(copyRoot, "bootstrap")); err != nil {
		t.Fatal(err)
	}
	if err = VerifyTree(copyRoot, got); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(copyRoot, "bundle/Headroom.app/Contents/Frameworks/QtCore.framework/QtCore")
	if err = os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(link, header, 0o755); err != nil {
		t.Fatal(err)
	}
	if VerifyTree(copyRoot, got) == nil {
		t.Fatal("framework link replaced by regular file accepted")
	}
}

func TestMacOSFrameworkGraphRejectsMalformedLinks(t *testing.T) {
	for _, scenario := range []string{"missing", "cycle", "undeclared", "nested", "link-parent"} {
		t.Run(scenario, func(t *testing.T) {
			manifest := macOSManifestFixture()
			base := "bundle/Headroom.app/Contents/Frameworks/QtCore.framework/"
			for index := range manifest.Files {
				record := &manifest.Files[index]
				if record.Path != base+"Versions/Current" {
					continue
				}
				switch scenario {
				case "missing":
					manifest.Files = append(manifest.Files[:index], manifest.Files[index+1:]...)
				case "cycle":
					record.LinkTarget = "Current"
				case "undeclared":
					record.LinkTarget = "B"
				}
				if scenario != "missing" {
					record.Size, record.SHA256 = int64(len(record.LinkTarget)), hashString(record.LinkTarget)
				}
				break
			}
			if scenario == "nested" {
				name := base + "Versions/A/Nested.framework/Versions/Current"
				target := "A"
				manifest.Files = append(manifest.Files, File{Path: name, LinkTarget: target, Size: 1, SHA256: hashString(target)})
			}
			if scenario == "link-parent" {
				manifest.Files = append(manifest.Files, File{Path: base + "Resources/smuggled", Size: 1, SHA256: strings.Repeat("a", 64), Mode: "0644"})
			}
			sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
			if ValidateManifest(manifest) == nil {
				t.Fatal("invalid framework accepted")
			}
		})
	}
}

func TestInstalledDirectoryLinkCannotRetainTrust(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory symlink fixture")
	}
	paths := []string{"bin"}
	if runtime.GOOS == "darwin" {
		paths = []string{"Headroom.app", "Headroom.app/Contents/Frameworks/QtCore.framework/Versions/A"}
	}
	for _, relative := range paths {
		t.Run(relative, func(t *testing.T) {
			root := t.TempDir()
			archive := makePackage(t, root, "2.3.4", false)
			install := filepath.Join(root, "install")
			state, err := InstallArchive(archive, install, filepath.Join(root, "entry/headroom"), Expectations{})
			if err != nil {
				t.Fatal(err)
			}
			original := filepath.Join(install, filepath.FromSlash(state.VersionPath), filepath.FromSlash(relative))
			outside := filepath.Join(root, "outside")
			if err = os.Rename(original, outside); err != nil {
				t.Fatal(err)
			}
			if err = os.Symlink(outside, original); err != nil {
				t.Fatal(err)
			}
			if InspectInstall(install).TrustedIdentity {
				t.Fatal("directory symlink retained trusted identity")
			}
			if _, _, err = ActiveExecutable(install); err == nil {
				t.Fatal("directory symlink authorized launch")
			}
		})
	}
}
