package main

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"

	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/contract"
)

func buildPackage(args []string) error {
	set := flag.NewFlagSet("package", flag.ContinueOnError)
	output := set.String("output", "", "disposable package directory")
	manager := set.String("manager", "", "real built headroom-package executable")
	version := set.String("version", "41.0.0", "synthetic stable version")
	kind := set.String("kind", "", "empty desktop or cli")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *output == "" || !filepath.IsAbs(*output) || !filepath.IsAbs(*manager) {
		return errors.New("output and manager must be absolute paths")
	}
	platform, architecture, err := contract.NativeTarget()
	if err != nil {
		return err
	}
	asset, err := contract.AssetNameForKind(*kind, *version, platform, architecture)
	if err != nil {
		return err
	}
	rootName := strings.TrimSuffix(strings.TrimSuffix(asset, ".tar.gz"), ".zip")
	root := filepath.Join(*output, rootName)
	ext := ""
	if platform == "windows" {
		ext = ".exe"
	}
	application := "bundle/bin/headroom" + ext
	server := "bundle/bin/usage-server" + ext
	cli := "bundle/bin/headroom-cli" + ext
	if platform == "macos" && *kind != contract.PackageKindCLI {
		application = "bundle/Headroom.app/Contents/MacOS/headroom"
		server = "bundle/Headroom.app/Contents/MacOS/usage-server"
		cli = "bundle/Headroom.app/Contents/MacOS/headroom-cli"
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	files := map[string]string{
		application: self, server: self, cli: self, "bundle/bin/headroom-package" + ext: *manager,
		"bootstrap/headroom" + ext: *manager, "bootstrap/headroom-package" + ext: *manager,
	}
	if *kind != contract.PackageKindCLI {
		files["bootstrap/headroom-cli"+ext] = *manager
		files["bundle/bin/headroom-cli-launcher"+ext] = *manager
		if platform == "windows" {
			files["bundle/bin/headroom-credential-helper.exe"] = self
		}
	}
	for name, source := range files {
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		if err = putFixtureFile(root, name, data, 0o755); err != nil {
			return err
		}
	}
	for _, name := range fixtureRuntime(platform, *kind) {
		if err = putFixtureFile(root, name, []byte("synthetic runtime\n"), 0o644); err != nil {
			return err
		}
	}
	qt := "6.12.0"
	if *kind == contract.PackageKindCLI {
		qt = ""
	}
	manifest, err := contract.BuildManifestForKind(*kind, root, *version, platform, architecture, qt, "synthetic")
	if err != nil {
		return err
	}
	if err = contract.WriteJSON(filepath.Join(root, contract.PackageManifestName), manifest); err != nil {
		return err
	}
	archive := filepath.Join(*output, asset)
	if err = contract.WriteArchive(root, archive); err != nil {
		return err
	}
	if _, _, err = contract.VerifyArchive(archive, contract.Expectations{}); err != nil {
		return err
	}
	size, hash, err := contract.FileDigest(archive)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Archive string                  `json:"archive"`
		Package contract.ReleasePackage `json:"package"`
	}{archive, contract.ReleasePackage{Platform: platform, Architecture: architecture, AssetName: asset,
		Size: size, SHA256: hash, PackageManifestPath: rootName + "/" + contract.PackageManifestName, Components: manifest.Components}})
}

func putFixtureFile(root, name string, data []byte, mode os.FileMode) error {
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, mode)
}

func fixtureRuntime(platform, kind string) []string {
	files := []string{"bundle/share/headroom/THIRD_PARTY_NOTICES.txt", "bundle/share/licenses/headroom/LICENSE"}
	if kind == contract.PackageKindCLI {
		return files
	}
	files = append(files, "bundle/share/licenses/qt/attributions/index.json")
	files = append(files, "bundle/share/icons/hicolor/scalable/apps/headroom.svg")
	switch platform {
	case "linux":
		return append(files, "bundle/qml/QtQuick/Controls/Basic/qmldir", "bundle/plugins/platforms/libqxcb.so", "bundle/plugins/platforms/libqwayland-generic.so", "bundle/plugins/platforms/libqoffscreen.so", "bundle/plugins/tls/libqopensslbackend.so", "bundle/plugins/imageformats/libqsvg.so", "bundle/plugins/iconengines/libqsvgicon.so")
	case "windows":
		return append(files, "bundle/qml/QtQuick/Controls/Basic/qmldir", "bundle/bin/msvcp140.dll", "bundle/bin/vcruntime140.dll", "bundle/plugins/platforms/qwindows.dll", "bundle/plugins/platforms/qoffscreen.dll", "bundle/plugins/tls/qschannelbackend.dll", "bundle/plugins/imageformats/qsvg.dll", "bundle/plugins/iconengines/qsvgicon.dll")
	case "macos":
		return append(files, "bundle/Headroom.app/Contents/Info.plist", "bundle/Headroom.app/Contents/Resources/headroom.icns", "bundle/Headroom.app/Contents/Frameworks/QtCore.framework/Versions/A/QtCore", "bundle/Headroom.app/Contents/Frameworks/QtCore.framework/Versions/A/Resources/Info.plist", "bundle/Headroom.app/Contents/PlugIns/platforms/libqcocoa.dylib", "bundle/Headroom.app/Contents/PlugIns/platforms/libqoffscreen.dylib", "bundle/Headroom.app/Contents/PlugIns/tls/libqsecuretransportbackend.dylib", "bundle/Headroom.app/Contents/PlugIns/imageformats/libqsvg.dylib", "bundle/Headroom.app/Contents/PlugIns/iconengines/libqsvgicon.dylib", "bundle/Headroom.app/Contents/Resources/qml/QtQuick/Controls/Basic/qmldir")
	default:
		return files
	}
}
