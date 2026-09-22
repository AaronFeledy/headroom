package contract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type updateFixture struct {
	kind              string
	t                 *testing.T
	server            *httptest.Server
	version           string
	archive           string
	legacy            bool
	badHash           bool
	slow              bool
	status            int
	prerelease        bool
	trailing          bool
	duplicate         bool
	manifestSizeDelta int64
	packageMode       string
	packageURL        string
	manifestVersion   string
	malformedManifest bool
	delay             time.Duration
	redirectTarget    string
	foreignOnly       bool
	malformedMetadata bool
	mu                sync.Mutex
	paths             []string
	authorizationSeen bool
}

func newUpdateFixture(t *testing.T, version, archive string) *updateFixture {
	f := &updateFixture{t: t, version: version, archive: archive}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *updateFixture) client() UpdateClient {
	origin, _ := url.Parse(f.server.URL)
	return UpdateClient{ReleaseAPI: f.server.URL + "/repos/AaronFeledy/headroom/releases/latest", FixtureOrigin: origin, Timeout: 30 * time.Second}
}

func (f *updateFixture) serve(writer http.ResponseWriter, request *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, request.URL.Path)
	f.authorizationSeen = f.authorizationSeen || request.Header.Get("Authorization") != ""
	f.mu.Unlock()
	if f.slow {
		time.Sleep(200 * time.Millisecond)
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if strings.HasSuffix(request.URL.Path, "/latest") || strings.Contains(request.URL.Path, "/tags/") {
		if f.status != 0 {
			writer.WriteHeader(f.status)
			return
		}
		if f.malformedMetadata {
			_, _ = writer.Write([]byte("{broken"))
			return
		}
		if f.legacy {
			_ = json.NewEncoder(writer).Encode(githubRelease{TagName: "v1.7.1", Assets: []githubAsset{{Name: "ClaudeUsageWidget-win-x64.exe", URL: f.server.URL + "/legacy", Size: 1}}})
			return
		}
		manifest, archiveBytes := f.release()
		manifestBytes, _ := json.Marshal(manifest)
		packageURL := f.packageURL
		if packageURL == "" {
			packageURL = f.server.URL + "/download/package"
		}
		manifestSuffix := "-release.json"
		if platform, _, _ := NativeTarget(); platform == "macos" {
			manifestSuffix = "-release-all.json"
		}
		manifestPrefix := "Headroom-v"
		platform, architecture, _ := NativeTarget()
		assetName, _ := AssetNameForKind(f.kind, f.version, platform, architecture)
		if f.kind == PackageKindCLI {
			manifestPrefix, manifestSuffix = "Headroom-CLI-v", "-release.json"
		}
		assets := []githubAsset{
			{Name: manifestPrefix + f.version + manifestSuffix, URL: f.server.URL + "/download/release", Size: int64(len(manifestBytes)) + f.manifestSizeDelta},
			{Name: assetName, URL: packageURL, Size: int64(len(archiveBytes))},
		}
		if f.foreignOnly {
			platform, architecture, _ := NativeTarget()
			foreignPlatform, foreignArchitecture := "windows", "arm64"
			if platform == foreignPlatform && architecture == foreignArchitecture {
				foreignArchitecture = "x86_64"
			}
			foreign, _ := AssetName(f.version, foreignPlatform, foreignArchitecture)
			assets[1].Name = foreign
		}
		if f.duplicate {
			assets = append(assets, assets[0])
		}
		metadata, _ := json.Marshal(githubRelease{TagName: "v" + f.version, Prerelease: f.prerelease, Assets: assets})
		_, _ = writer.Write(metadata)
		if f.trailing {
			_, _ = writer.Write([]byte(" trailing"))
		}
		return
	}
	manifest, archiveBytes := f.release()
	if strings.HasSuffix(request.URL.Path, "/release") {
		if f.malformedManifest {
			_, _ = writer.Write([]byte("{broken"))
			return
		}
		bytes, _ := json.Marshal(manifest)
		_, _ = writer.Write(bytes)
		return
	}
	if strings.HasSuffix(request.URL.Path, "/redirect") {
		http.Redirect(writer, request, f.server.URL+"/download/package", http.StatusFound)
		return
	}
	if strings.HasSuffix(request.URL.Path, "/redirect-foreign") {
		http.Redirect(writer, request, f.redirectTarget, http.StatusFound)
		return
	}
	if strings.HasSuffix(request.URL.Path, "/package") {
		if f.packageMode == "cancel" {
			writer.Header().Set("Content-Length", fmt.Sprint(len(archiveBytes)))
			flusher, _ := writer.(http.Flusher)
			for offset := 0; offset < len(archiveBytes); offset += 4096 {
				end := offset + 4096
				if end > len(archiveBytes) {
					end = len(archiveBytes)
				}
				if _, err := writer.Write(archiveBytes[offset:end]); err != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
				time.Sleep(10 * time.Millisecond)
			}
			return
		}
		if f.packageMode == "truncated" {
			archiveBytes = archiveBytes[:len(archiveBytes)/2]
		}
		if f.packageMode == "oversized" {
			archiveBytes = append(archiveBytes, 'x')
		}
		_, _ = writer.Write(archiveBytes)
		return
	}
	http.NotFound(writer, request)
}

func (f *updateFixture) requested(fragment string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, item := range f.paths {
		if strings.Contains(item, fragment) {
			return true
		}
	}
	return false
}

func (f *updateFixture) release() (ReleaseManifest, []byte) {
	archiveBytes, err := os.ReadFile(f.archive)
	if err != nil {
		f.t.Fatal(err)
	}
	digest := sha256.Sum256(archiveBytes)
	hash := hex.EncodeToString(digest[:])
	if f.badHash {
		hash = strings.Repeat("0", 64)
	}
	platform, architecture, _ := NativeTarget()
	releaseVersion := f.version
	if f.manifestVersion != "" {
		releaseVersion = f.manifestVersion
	}
	release := ReleaseManifest{PackageKind: f.kind, Schema: 1, Product: "Headroom", Version: releaseVersion}
	targets := [][2]string{{"linux", "x86_64"}, {"windows", "arm64"}, {"windows", "x86_64"}, {"macos", "x86_64"}, {"macos", "arm64"}}
	if f.kind == PackageKindCLI {
		targets = append(targets, [2]string{"linux", "arm64"})
	}
	for _, target := range targets {
		asset, _ := AssetNameForKind(f.kind, releaseVersion, target[0], target[1])
		root, _ := ArchiveRootForKind(f.kind, releaseVersion, target[0], target[1])
		components := fixtureComponentsForKind(releaseVersion, target[0], f.kind)
		size, sum := int64(1), strings.Repeat("1", 64)
		if releaseVersion == f.version && target[0] == platform && target[1] == architecture {
			size, sum, components = int64(len(archiveBytes)), hash, fixtureComponentsForKind(f.version, platform, f.kind)
		}
		release.Packages = append(release.Packages, ReleasePackage{Platform: target[0], Architecture: target[1], AssetName: asset,
			Size: size, SHA256: sum, PackageManifestPath: root + "/" + PackageManifestName, Components: components})
	}
	return release, archiveBytes
}

func fixtureComponents(version, platform string) Components {
	return fixtureComponentsForKind(version, platform, "")
}

func fixtureComponentsForKind(version, platform, kind string) Components {
	ext := ""
	if platform == "windows" {
		ext = ".exe"
	}
	components := Components{Application: Component{packageApplicationPath(platform), version}, Server: Component{packageServerPath(platform), version},
		Launcher: Component{"bootstrap/headroom" + ext, version}, Manager: Component{"bootstrap/headroom-package" + ext, version}}
	if platform == "windows" {
		helper := Component{"bundle/bin/headroom-credential-helper.exe", version}
		components.CredentialHelper = &helper
	}
	if kind == PackageKindCLI {
		components.Application.Path = "bundle/bin/headroom" + ext
		components.Server = components.Application
		components.CredentialHelper = nil
	}
	return components
}

func nativeAsset(version string) string {
	platform, architecture, _ := NativeTarget()
	name, _ := AssetName(version, platform, architecture)
	return name
}

func installFixture(t *testing.T, parent, version string) (string, string) {
	archive := makePackage(t, parent, version, false)
	root := filepath.Join(parent, "installed")
	entry := filepath.Join(parent, "entry", "headroom"+nativeExtension())
	if _, err := InstallArchive(archive, root, entry, Expectations{}); err != nil {
		t.Fatal(err)
	}
	return root, archive
}

func TestUpdateStagesVerifiedNewerPackageWithoutChangingActive(t *testing.T) {
	parent := t.TempDir()
	root, _ := installFixture(t, parent, "1.2.3")
	newArchive := makePackage(t, parent, "1.2.4", false)
	fixture := newUpdateFixture(t, "1.2.4", newArchive)
	checked, err := fixture.client().Check(context.Background(), root)
	if err != nil || checked.Status != "available" {
		t.Fatalf("check = %+v, %v", checked, err)
	}
	staged, err := fixture.client().StageLatest(context.Background(), root)
	if err != nil || staged.Status != "staged" || staged.Stage == nil {
		t.Fatalf("stage = %+v, %v", staged, err)
	}
	if got := InspectInstall(root).Version; got != "1.2.3" {
		t.Fatalf("active version changed to %s", got)
	}
	record := filepath.Join(filepath.Dir(filepath.Dir(staged.Stage.PackageRoot)), "verified-stage.json")
	if _, err = os.Stat(record); err != nil {
		t.Fatalf("verified stage missing: %v", err)
	}
}

func TestUpdateRejectsLegacyAndBadDigest(t *testing.T) {
	parent := t.TempDir()
	root, _ := installFixture(t, parent, "1.2.3")
	archive := makePackage(t, parent, "1.2.4", false)
	fixture := newUpdateFixture(t, "1.2.4", archive)
	fixture.legacy = true
	result, err := fixture.client().Check(context.Background(), root)
	if err != nil || result.Status != "unavailable" {
		t.Fatalf("legacy = %+v, %v", result, err)
	}
	fixture.legacy = false
	fixture.badHash = true
	before, _ := os.ReadDir(filepath.Join(root, "staging"))
	if _, err = fixture.client().StageLatest(context.Background(), root); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("bad digest accepted: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "staging"))
	if len(entries) != len(before) {
		t.Fatalf("failed download left staged data: %v", entries)
	}
}

func TestRepairPinsInstalledVersionAndRequiresIncompleteTrustedInstall(t *testing.T) {
	parent := t.TempDir()
	root, archive := installFixture(t, parent, "8.4.2")
	fixture := newUpdateFixture(t, "8.4.2", archive)
	if _, err := fixture.client().StageRepair(context.Background(), root); err == nil {
		t.Fatal("complete install offered repair")
	}
	inspection := InspectInstall(root)
	server := installedTestComponent(t, root, inspection.VersionPath, true)
	if err := os.Remove(server); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.client().StageRepair(context.Background(), root)
	if err != nil || result.Status != "staged" || result.Version != "8.4.2" {
		t.Fatalf("repair = %+v, %v", result, err)
	}
	if InspectInstall(root).Complete {
		t.Fatal("staging repair modified active install")
	}
	if _, err = fixture.client().StageRepair(context.Background(), filepath.Join(parent, "source")); err == nil {
		t.Fatal("untrusted source install staged repair")
	}
}

func TestUpdateCancellationAndSafeLargeSemver(t *testing.T) {
	parent := t.TempDir()
	root, _ := installFixture(t, parent, "1.2.3")
	archive := makePackage(t, parent, "1.2.4", false)
	fixture := newUpdateFixture(t, "1.2.4", archive)
	fixture.slow = true
	client := fixture.client()
	client.Timeout = 40 * time.Millisecond
	if _, err := client.Check(context.Background(), root); err == nil {
		t.Fatal("timed out update check succeeded")
	}
	left := "999999999999999999999999999999.2.3"
	right := "1000000000000000000000000000000.0.0"
	if comparison, err := CompareVersions(left, right); err != nil || comparison >= 0 {
		t.Fatalf("comparison = %d, %v", comparison, err)
	}
	if _, err := CompareVersions("1.2.3-beta", "1.2.3"); err == nil {
		t.Fatal("prerelease compared as stable")
	}
}

func TestStartupCheckCleansOnlyOldIncompleteOwnedOperations(t *testing.T) {
	parent := t.TempDir()
	root, _ := installFixture(t, parent, "1.2.3")
	staging := filepath.Join(root, "staging")
	incomplete := filepath.Join(staging, "package-incomplete")
	verified := filepath.Join(staging, "package-verified")
	young := filepath.Join(root, ".headroom-download-young")
	oldDownload := filepath.Join(root, ".headroom-download-old")
	for _, path := range []string{incomplete, verified, young, oldDownload} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(verified, "verified-stage.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	for _, path := range []string{incomplete, verified, oldDownload} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	archive := makePackage(t, parent, "1.2.4", false)
	fixture := newUpdateFixture(t, "1.2.4", archive)
	fixture.status = http.StatusNotFound
	if result, err := fixture.client().Check(context.Background(), root); err != nil || result.Status != "unavailable" {
		t.Fatalf("offline startup check = %+v, %v", result, err)
	}
	for _, path := range []string{verified, young} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("preserved operation %s: %v", path, err)
		}
	}
	for _, path := range []string{incomplete, oldDownload} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("abandoned operation remains at %s: %v", path, err)
		}
	}
}

func TestUpdateMetadataFailuresAndNoPackageFetchForNonNewer(t *testing.T) {
	parent := t.TempDir()
	root, _ := installFixture(t, parent, "1.2.3")
	archive := makePackage(t, parent, "1.2.4", false)
	for _, test := range []struct {
		name        string
		configure   func(*updateFixture)
		unavailable bool
	}{
		{"not-found", func(f *updateFixture) { f.status = http.StatusNotFound }, true},
		{"rate-limit", func(f *updateFixture) { f.status = http.StatusForbidden }, false},
		{"prerelease", func(f *updateFixture) { f.prerelease = true }, true},
		{"trailing-json", func(f *updateFixture) { f.trailing = true }, false},
		{"malformed-metadata", func(f *updateFixture) { f.malformedMetadata = true }, false},
		{"duplicate", func(f *updateFixture) { f.duplicate = true }, true},
		{"manifest-size", func(f *updateFixture) { f.manifestSizeDelta = 1 }, false},
		{"malformed-manifest", func(f *updateFixture) { f.malformedManifest = true }, false},
		{"manifest-version", func(f *updateFixture) { f.manifestVersion = "1.2.5" }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newUpdateFixture(t, "1.2.4", archive)
			test.configure(fixture)
			result, err := fixture.client().Check(context.Background(), root)
			if test.unavailable {
				if err != nil || result.Status != "unavailable" {
					t.Fatalf("result = %+v, %v", result, err)
				}
			} else if err == nil {
				t.Fatalf("metadata failure accepted: %+v", result)
			}
			if fixture.requested("/package") {
				t.Fatal("package downloaded while checking metadata")
			}
			if fixture.authorizationSeen {
				t.Fatal("public request contained authorization")
			}
		})
	}
	for _, version := range []string{"1.2.3", "1.2.2"} {
		t.Run("non-newer-"+version, func(t *testing.T) {
			archive := makePackage(t, t.TempDir(), version, false)
			fixture := newUpdateFixture(t, version, archive)
			result, err := fixture.client().StageLatest(context.Background(), root)
			if err != nil || result.Status != "current" {
				t.Fatalf("result = %+v, %v", result, err)
			}
			if fixture.requested("/package") {
				t.Fatal("non-newer package was downloaded")
			}
		})
	}
}

func TestWrongTargetAndInvalidPackageAreRejectedBeforeActiveInstall(t *testing.T) {
	parent := t.TempDir()
	root, _ := installFixture(t, parent, "1.2.3")
	archive := makePackage(t, parent, "1.2.4", false)
	foreign := newUpdateFixture(t, "1.2.4", archive)
	foreign.foreignOnly = true
	if _, err := foreign.client().StageLatest(context.Background(), root); err == nil {
		t.Fatal("foreign OS/CPU asset selected")
	}
	if foreign.requested("/package") {
		t.Fatal("foreign package was downloaded")
	}
	broken := makePackage(t, t.TempDir(), "1.2.4", true)
	invalid := newUpdateFixture(t, "1.2.4", broken)
	if _, err := invalid.client().StageLatest(context.Background(), root); err == nil {
		t.Fatal("invalid package was staged")
	}
	if InspectInstall(root).Version != "1.2.3" {
		t.Fatal("invalid package changed active install")
	}
}

func TestRepairUsesExactTagAndPublicRequestsHaveNoAuthorization(t *testing.T) {
	parent := t.TempDir()
	root, archive := installFixture(t, parent, "8.4.2")
	inspection := InspectInstall(root)
	server := installedTestComponent(t, root, inspection.VersionPath, true)
	if err := os.Remove(server); err != nil {
		t.Fatal(err)
	}
	fixture := newUpdateFixture(t, "8.4.2", archive)
	if _, err := fixture.client().StageRepair(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if !fixture.requested("/releases/tags/v8.4.2") || fixture.requested("/releases/latest") {
		t.Fatalf("repair paths = %v", fixture.paths)
	}
	if fixture.authorizationSeen {
		t.Fatal("public repair request contained authorization")
	}
}

func TestUpdateRedirectAndStreamingFailuresLeaveNoStage(t *testing.T) {
	parent := t.TempDir()
	root, _ := installFixture(t, parent, "1.2.3")
	archive := makePackage(t, parent, "1.2.4", false)
	fixture := newUpdateFixture(t, "1.2.4", archive)
	fixture.packageURL = fixture.server.URL + "/download/redirect"
	if result, err := fixture.client().StageLatest(context.Background(), root); err != nil || result.Status != "staged" {
		t.Fatalf("allowed redirect = %+v, %v", result, err)
	}
	for _, mode := range []string{"truncated", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			base := t.TempDir()
			otherRoot, _ := installFixture(t, base, "1.2.3")
			broken := newUpdateFixture(t, "1.2.4", archive)
			broken.packageMode = mode
			if _, err := broken.client().StageLatest(context.Background(), otherRoot); err == nil {
				t.Fatal("broken stream staged")
			}
			if InspectInstall(otherRoot).Version != "1.2.3" {
				t.Fatal("active install changed")
			}
		})
	}
}

func TestProductionRedirectPolicyRejectsForeignDowngradePortAndPath(t *testing.T) {
	client := DefaultUpdateClient()
	for _, raw := range []string{
		"http://github.com/AaronFeledy/headroom/releases/download/v1/a",
		"https://evil.example/a",
		"https://github.com:444/AaronFeledy/headroom/releases/download/v1/a",
		"https://github.com/another/repository/releases/download/v1/a",
	} {
		candidate, _ := url.Parse(raw)
		if err := client.validateRedirectURL(candidate); err == nil {
			t.Fatalf("allowed redirect %s", raw)
		}
	}
	for _, raw := range []string{
		"https://github.com/AaronFeledy/headroom/releases/download/v1/a",
		"https://github.com/AaronFeledy/claude-usage-widget/releases/download/v1/a",
		"https://release-assets.githubusercontent.com/signed?token=value",
	} {
		candidate, _ := url.Parse(raw)
		if err := client.validateRedirectURL(candidate); err != nil {
			t.Fatalf("rejected redirect %s: %v", raw, err)
		}
	}
}

func TestProductionReleaseURLsAcceptCanonicalAndLegacyRepositories(t *testing.T) {
	client := DefaultUpdateClient()
	version := "1.2.3"
	name := "Headroom-v1.2.3-linux-x86_64.tar.gz"
	for _, repository := range []string{canonicalRepository, legacyRepository} {
		api, _ := url.Parse("https://api.github.com/repos/" + repository + "/releases/latest")
		if !client.validAPIURL(api) {
			t.Fatalf("rejected release API for %s", repository)
		}
		asset := "https://github.com/" + repository + "/releases/download/v" + version + "/" + name
		if err := client.validateAssetURL(asset, version, name); err != nil {
			t.Fatalf("rejected release asset for %s: %v", repository, err)
		}
	}
	foreignAPI, _ := url.Parse("https://api.github.com/repos/another/repository/releases/latest")
	if client.validAPIURL(foreignAPI) {
		t.Fatal("accepted foreign release API")
	}
	extraPath := "https://github.com/" + canonicalRepository + "/releases/download/v" + version + "/" + name + ".extra"
	if err := client.validateAssetURL(extraPath, version, name); err == nil {
		t.Fatal("accepted release asset with an appended path suffix")
	}
}

func TestBuildMetadataAssetURLAcceptsCanonicalPercentEncoding(t *testing.T) {
	client := DefaultUpdateClient()
	version := "1.2.3+build-linux"
	name := "Headroom-v1.2.3+build-linux-linux-x86_64.tar.gz"
	raw := "https://github.com/AaronFeledy/headroom/releases/download/v1.2.3%2Bbuild-linux/Headroom-v1.2.3%2Bbuild-linux-linux-x86_64.tar.gz"
	if err := client.validateAssetURL(raw, version, name); err != nil {
		t.Fatalf("valid encoded build metadata rejected: %v", err)
	}
	if comparison, err := CompareVersions(version, "1.2.3"); err != nil || comparison != 0 {
		t.Fatalf("build metadata comparison = %d, %v", comparison, err)
	}
}

func TestForeignRedirectIsRejectedBeforeTargetRequest(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	parent := t.TempDir()
	root, _ := installFixture(t, parent, "1.2.3")
	archive := makePackage(t, parent, "1.2.4", false)
	fixture := newUpdateFixture(t, "1.2.4", archive)
	fixture.packageURL = fixture.server.URL + "/download/redirect-foreign"
	fixture.redirectTarget = target.URL + "/stolen"
	if _, err := fixture.client().StageLatest(context.Background(), root); err == nil {
		t.Fatal("foreign redirect succeeded")
	}
	if hits.Load() != 0 {
		t.Fatal("foreign redirect target received a request")
	}
}

func TestAggregateDeadlineAndStreamingCancellationCleanPrivateDownload(t *testing.T) {
	parent := t.TempDir()
	root, _ := installFixture(t, parent, "1.2.3")
	archive := makePackage(t, parent, "1.2.4", false)
	fixture := newUpdateFixture(t, "1.2.4", archive)
	fixture.delay = 60 * time.Millisecond
	client := fixture.client()
	client.Timeout = 140 * time.Millisecond
	started := time.Now()
	if _, err := client.StageLatest(context.Background(), root); err == nil {
		t.Fatal("aggregate deadline did not stop update")
	}
	if time.Since(started) > 350*time.Millisecond {
		t.Fatal("each request received a fresh timeout budget")
	}
	cancelFixture := newUpdateFixture(t, "1.2.4", archive)
	cancelFixture.packageMode = "cancel"
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(5 * time.Millisecond); cancel() }()
	if _, err := cancelFixture.client().StageLatest(ctx, root); err == nil {
		t.Fatal("cancelled stream staged")
	}
	leftovers, _ := filepath.Glob(filepath.Join(root, ".headroom-download-*"))
	if len(leftovers) != 0 {
		t.Fatalf("cancelled download left private data: %v", leftovers)
	}
	if InspectInstall(root).Version != "1.2.3" {
		t.Fatal("cancel changed active install")
	}
}

func TestUpdaterRejectsLinkedStagingRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture is covered by Windows path validation tests")
	}
	parent := t.TempDir()
	root, _ := installFixture(t, parent, "1.2.3")
	archive := makePackage(t, parent, "1.2.4", false)
	escape := filepath.Join(parent, "escape")
	if err := os.Mkdir(escape, 0o700); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(escape, "package-old", "outside-data")
	if err := os.MkdirAll(external, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Dir(external), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "staging")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(escape, filepath.Join(root, "staging")); err != nil {
		t.Fatal(err)
	}
	fixture := newUpdateFixture(t, "1.2.4", archive)
	if _, err := fixture.client().StageLatest(context.Background(), root); err == nil || (!strings.Contains(err.Error(), "link") && !strings.Contains(err.Error(), "unsafe")) {
		t.Fatalf("linked staging accepted: %v", err)
	}
	if _, err := os.Stat(external); err != nil {
		t.Fatalf("cleanup traversed staging link and removed external data: %v", err)
	}
}

func TestUpdateReusesVerifiedDownloadAcrossClients(t *testing.T) {
	parent := t.TempDir()
	root, _ := installFixture(t, parent, "1.2.3")
	fixture := newUpdateFixture(t, "1.2.4", makePackage(t, parent, "1.2.4", false))
	first, err := fixture.client().StageLatest(context.Background(), root)
	if err != nil || first.Stage == nil {
		t.Fatalf("first stage: %+v, %v", first, err)
	}
	fixture.mu.Lock()
	fixture.paths = nil
	fixture.mu.Unlock()
	// Each call uses a fresh client, just like another manager process on launch.
	for _, action := range []string{"check", "latest", "exact"} {
		var result UpdateResult
		switch action {
		case "check":
			result, err = fixture.client().Check(context.Background(), root)
		case "latest":
			result, err = fixture.client().StageLatest(context.Background(), root)
		case "exact":
			result, err = fixture.client().StageVersion(context.Background(), root, "1.2.4")
		}
		if err != nil || result.Status != "staged" || result.Stage == nil || *result.Stage != *first.Stage {
			t.Fatalf("%s did not reuse stage: %+v, %v", action, result, err)
		}
	}
	if fixture.requested("/package") {
		t.Fatal("cached package was downloaded again")
	}
	if !fixture.requested("/release") {
		t.Fatal("release metadata was not refreshed")
	}
	if got := InspectInstall(root).Version; got != "1.2.3" {
		t.Fatalf("active version changed: %s", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	release, _ := fixture.release()
	var pkg ReleasePackage
	for _, candidate := range release.Packages {
		if candidate.AssetName == first.AssetName {
			pkg = candidate
		}
	}
	if _, err := reusableUpdateStage(ctx, root, release, pkg); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestUpdateRejectsInvalidCachedStages(t *testing.T) {
	for _, damage := range []string{"missing-file", "changed-file", "extra-file", "broken-record", "archive-hash", "archive-size", "old-record", "wrong-kind", "wrong-version", "wrong-platform", "outside-root"} {
		t.Run(damage, func(t *testing.T) {
			parent := t.TempDir()
			root, _ := installFixture(t, parent, "1.2.3")
			fixture := newUpdateFixture(t, "1.2.4", makePackage(t, parent, "1.2.4", false))
			first, err := fixture.client().StageLatest(context.Background(), root)
			if err != nil || first.Stage == nil {
				t.Fatalf("first stage: %+v, %v", first, err)
			}
			stage := *first.Stage
			record := stageRecord(stage)
			_, manifest, err := LoadVerifiedStage(root, record)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(stage.PackageRoot, filepath.FromSlash(manifest.Components.Server.Path))
			switch damage {
			case "missing-file":
				err = os.Remove(file)
			case "changed-file":
				var data []byte
				data, err = os.ReadFile(file)
				if err == nil {
					data[len(data)-1] ^= 1
					err = os.WriteFile(file, data, 0755)
				}
			case "extra-file":
				err = os.WriteFile(filepath.Join(stage.PackageRoot, "unexpected"), []byte("extra"), 0600)
			case "broken-record":
				err = os.WriteFile(record, []byte("{broken"), 0600)
			case "archive-hash":
				stage.ArchiveSHA256 = strings.Repeat("0", 64)
			case "archive-size":
				stage.ArchiveSize++
			case "old-record":
				stage.ArchiveSHA256, stage.ArchiveSize = "", 0
			case "wrong-kind":
				stage.PackageKind = PackageKindCLI
			case "wrong-version":
				stage.Version = "1.2.5"
			case "wrong-platform":
				stage.Platform = "other"
			case "outside-root":
				stage.PackageRoot = parent
			}
			if err != nil {
				t.Fatal(err)
			}
			if stage != *first.Stage {
				if err = WriteJSON(record, stage); err != nil {
					t.Fatal(err)
				}
			}
			fixture.mu.Lock()
			fixture.paths = nil
			fixture.mu.Unlock()
			checked, err := fixture.client().Check(context.Background(), root)
			if err != nil || checked.Status != "available" {
				t.Fatalf("damaged check: %+v, %v", checked, err)
			}
			if fixture.requested("/package") {
				t.Fatal("metadata check downloaded package")
			}
			second, err := fixture.client().StageLatest(context.Background(), root)
			if err != nil || second.Stage == nil || second.Stage.PackageRoot == first.Stage.PackageRoot {
				t.Fatalf("damaged stage reused: %+v, %v", second, err)
			}
			if !fixture.requested("/package") {
				t.Fatal("invalid cache did not trigger download")
			}
		})
	}
}

func TestUpdateDoesNotReuseStageAfterReleaseDigestChanges(t *testing.T) {
	parent := t.TempDir()
	root, _ := installFixture(t, parent, "1.2.3")
	fixture := newUpdateFixture(t, "1.2.4", makePackage(t, parent, "1.2.4", false))
	if _, err := fixture.client().StageLatest(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	fixture.badHash = true
	checked, err := fixture.client().Check(context.Background(), root)
	if err != nil || checked.Status != "available" {
		t.Fatalf("changed release: %+v, %v", checked, err)
	}
	if _, err := fixture.client().StageLatest(context.Background(), root); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("stale digest accepted: %v", err)
	}
}
