package contract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	canonicalRepository = "AaronFeledy/headroom"
	legacyRepository    = "AaronFeledy/claude-usage-widget"
	defaultReleaseAPI   = "https://api.github.com/repos/" + canonicalRepository + "/releases/latest"
	maxReleaseBytes     = int64(4 << 20)
)

// UpdateResult is the stable machine-readable result used by the desktop UI.
// Status is one of unavailable, current, available, or staged.
type UpdateResult struct {
	Status         string       `json:"status"`
	CurrentVersion string       `json:"current_version"`
	Version        string       `json:"version,omitempty"`
	Platform       string       `json:"platform"`
	Architecture   string       `json:"architecture"`
	AssetName      string       `json:"asset_name,omitempty"`
	Reason         string       `json:"reason,omitempty"`
	Stage          *StageResult `json:"stage,omitempty"`
}

// UpdateClient permits an isolated HTTP fixture in contract tests. Production
// callers use DefaultUpdateClient, whose origins cannot be overridden by CLI
// arguments or environment variables.
type UpdateClient struct {
	ReleaseAPI    string
	FixtureOrigin *url.URL
	Timeout       time.Duration
}

func DefaultUpdateClient() UpdateClient {
	return UpdateClient{ReleaseAPI: defaultReleaseAPI, Timeout: 2 * time.Minute}
}

type githubRelease struct {
	TagName    string        `json:"tag_name"`
	Draft      bool          `json:"draft"`
	Prerelease bool          `json:"prerelease"`
	Assets     []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

func (c UpdateClient) Check(ctx context.Context, installRoot string) (UpdateResult, error) {
	ctx, cancel := c.operationContext(ctx)
	defer cancel()
	inspection, root, err := trustedUpdateInstall(installRoot)
	if err != nil {
		return UpdateResult{}, err
	}
	CleanupAbandoned(root, time.Now().Add(-time.Hour))
	release, pkg, _, err := c.resolve(ctx, "", inspection)
	if errors.Is(err, errNoCompatibleRelease) {
		return updateResult(inspection, "unavailable", "compatible_release_missing"), nil
	}
	if err != nil {
		return UpdateResult{}, err
	}
	result := updateResult(inspection, "current", "")
	result.Version, result.AssetName = release.Version, pkg.AssetName
	comparison, err := CompareVersions(release.Version, inspection.Version)
	if err != nil {
		return UpdateResult{}, err
	}
	if comparison > 0 {
		result.Status = "available"
		stage, err := reusableUpdateStage(ctx, root, release, pkg)
		if err != nil {
			return UpdateResult{}, err
		}
		if stage != nil {
			result.Status, result.Stage = "staged", stage
		}
	}
	return result, nil
}

func (c UpdateClient) StageLatest(ctx context.Context, installRoot string) (UpdateResult, error) {
	ctx, cancel := c.operationContext(ctx)
	defer cancel()
	inspection, root, err := trustedUpdateInstall(installRoot)
	if err != nil {
		return UpdateResult{}, err
	}
	CleanupAbandoned(root, time.Now().Add(-time.Hour))
	release, pkg, asset, err := c.resolve(ctx, "", inspection)
	if errors.Is(err, errNoCompatibleRelease) {
		return updateResult(inspection, "unavailable", "compatible_release_missing"), nil
	}
	if err != nil {
		return UpdateResult{}, err
	}
	comparison, err := CompareVersions(release.Version, inspection.Version)
	if err != nil {
		return UpdateResult{}, err
	}
	if comparison <= 0 {
		result := updateResult(inspection, "current", "")
		result.Version, result.AssetName = release.Version, pkg.AssetName
		return result, nil
	}
	return c.downloadAndStage(ctx, root, inspection, release, pkg, asset)
}

func (c UpdateClient) StageRepair(ctx context.Context, installRoot string) (UpdateResult, error) {
	ctx, cancel := c.operationContext(ctx)
	defer cancel()
	inspection, root, err := trustedUpdateInstall(installRoot)
	if err != nil {
		return UpdateResult{}, err
	}
	CleanupAbandoned(root, time.Now().Add(-time.Hour))
	if inspection.Complete {
		return UpdateResult{}, errors.New("Headroom installation does not need repair")
	}
	release, pkg, asset, err := c.resolve(ctx, inspection.Version, inspection)
	if errors.Is(err, errNoCompatibleRelease) {
		return UpdateResult{}, fmt.Errorf("the matching Headroom %s package is unavailable", inspection.Version)
	}
	if err != nil {
		return UpdateResult{}, err
	}
	if release.Version != inspection.Version {
		return UpdateResult{}, errors.New("repair release does not match the installed version")
	}
	return c.downloadAndStage(ctx, root, inspection, release, pkg, asset)
}

var errNoCompatibleRelease = errors.New("no compatible Headroom release")

func trustedUpdateInstall(installRoot string) (Inspection, string, error) {
	root, err := NormalizeInstallRoot(installRoot)
	if err != nil {
		return Inspection{}, "", err
	}
	if err = validateInstallTargets(root, ""); err != nil {
		return Inspection{}, "", err
	}
	inspection := InspectInstall(root)
	if !inspection.TrustedIdentity {
		return inspection, "", errors.New("Headroom installation identity is not valid")
	}
	nativePlatform, nativeArchitecture, err := NativeTarget()
	if err != nil || inspection.Platform != nativePlatform || inspection.Architecture != nativeArchitecture {
		return inspection, "", errors.New("Headroom installation does not match this computer")
	}
	return inspection, root, nil
}

func updateResult(inspection Inspection, status, reason string) UpdateResult {
	return UpdateResult{Status: status, CurrentVersion: inspection.Version, Platform: inspection.Platform,
		Architecture: inspection.Architecture, Reason: reason}
}

func (c UpdateClient) resolve(ctx context.Context, exactVersion string, inspection Inspection) (ReleaseManifest, ReleasePackage, githubAsset, error) {
	api := c.ReleaseAPI
	if api == "" {
		api = defaultReleaseAPI
	}
	if exactVersion != "" {
		api = strings.TrimSuffix(api, "/latest") + "/tags/v" + url.PathEscape(exactVersion)
	}
	body, err := c.fetch(ctx, api, maxReleaseBytes, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ReleaseManifest{}, ReleasePackage{}, githubAsset{}, errNoCompatibleRelease
		}
		return ReleaseManifest{}, ReleasePackage{}, githubAsset{}, err
	}
	var release githubRelease
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if err = decoder.Decode(&release); err != nil || ensureEOF(decoder) != nil || !strings.HasPrefix(release.TagName, "v") {
		return ReleaseManifest{}, ReleasePackage{}, githubAsset{}, errors.New("release metadata was not recognized")
	}
	if release.Draft || release.Prerelease {
		return ReleaseManifest{}, ReleasePackage{}, githubAsset{}, errNoCompatibleRelease
	}
	version := strings.TrimPrefix(release.TagName, "v")
	if !stableVersion(version) || (exactVersion != "" && version != exactVersion) {
		return ReleaseManifest{}, ReleasePackage{}, githubAsset{}, errNoCompatibleRelease
	}
	manifestName := "Headroom-v" + version + "-release.json"
	if inspection.Platform == "macos" {
		manifestName = "Headroom-v" + version + "-release-all.json"
	}
	if inspection.PackageKind == PackageKindCLI {
		manifestName = "Headroom-CLI-v" + version + "-release.json"
	}
	manifestAsset, ok := namedAsset(release.Assets, manifestName)
	if !ok || manifestAsset.Size <= 0 || manifestAsset.Size > maxReleaseBytes || c.validateAssetURL(manifestAsset.URL, version, manifestName) != nil {
		return ReleaseManifest{}, ReleasePackage{}, githubAsset{}, errNoCompatibleRelease
	}
	manifestBytes, err := c.fetch(ctx, manifestAsset.URL, maxReleaseBytes, true)
	if err != nil {
		return ReleaseManifest{}, ReleasePackage{}, githubAsset{}, err
	}
	if int64(len(manifestBytes)) != manifestAsset.Size {
		return ReleaseManifest{}, ReleasePackage{}, githubAsset{}, errors.New("release manifest size does not match release metadata")
	}
	manifest, err := DecodeReleaseManifest(strings.NewReader(string(manifestBytes)))
	if err != nil || manifest.Version != version || manifest.PackageKind != inspection.PackageKind {
		if err == nil {
			err = errors.New("release tag, package kind and manifest version do not match")
		}
		return ReleaseManifest{}, ReleasePackage{}, githubAsset{}, err
	}
	var selected ReleasePackage
	found := false
	for _, candidate := range manifest.Packages {
		if candidate.Platform == inspection.Platform && candidate.Architecture == inspection.Architecture {
			selected, found = candidate, true
			break
		}
	}
	if !found {
		return ReleaseManifest{}, ReleasePackage{}, githubAsset{}, errNoCompatibleRelease
	}
	asset, ok := namedAsset(release.Assets, selected.AssetName)
	if !ok || asset.Size != selected.Size || c.validateAssetURL(asset.URL, version, selected.AssetName) != nil {
		return ReleaseManifest{}, ReleasePackage{}, githubAsset{}, errors.New("release package asset does not match its manifest")
	}
	return manifest, selected, asset, nil
}

func namedAsset(assets []githubAsset, name string) (githubAsset, bool) {
	var result githubAsset
	found := false
	for _, asset := range assets {
		if asset.Name != name {
			continue
		}
		if found {
			return githubAsset{}, false
		}
		result, found = asset, true
	}
	return result, found
}

// reusableUpdateStage treats a prior download as a cache entry, never as proof
// that its files are still intact. Bind it to freshly fetched release metadata,
// then use the apply-time verifier to rehash the complete extracted package.
func reusableUpdateStage(ctx context.Context, root string, release ReleaseManifest, pkg ReleasePackage) (*StageResult, error) {
	staging := filepath.Join(root, "staging")
	// A replaced staging directory fails the operation instead of degrading to
	// a fresh download, because every caller would then write a package through
	// it. The per-entry anomalies below are ordinary cache misses.
	if err := validateInstallTargets(staging, ""); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(staging)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	matches := func(stage StageResult) bool {
		return stage.PackageKind == release.PackageKind && stage.Version == release.Version &&
			stage.Platform == pkg.Platform && stage.Architecture == pkg.Architecture &&
			stage.PackageAsset == pkg.AssetName && stage.ArchiveSHA256 == pkg.SHA256 && stage.ArchiveSize == pkg.Size
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "package-") {
			continue
		}
		record := filepath.Join(staging, entry.Name(), "verified-stage.json")
		if validateInstallTargets(record, "") != nil {
			continue
		}
		info, err := os.Lstat(record)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		data, err := readBoundedFile(record, maxManifestBytes)
		if err != nil {
			continue
		}
		var candidate StageResult
		if json.Unmarshal(data, &candidate) != nil || !matches(candidate) {
			continue
		}
		stage, _, err := LoadVerifiedStage(root, record)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err == nil && matches(stage) {
			return &stage, nil
		}
	}
	return nil, ctx.Err()
}

func (c UpdateClient) downloadAndStage(ctx context.Context, root string, inspection Inspection, release ReleaseManifest, pkg ReleasePackage, asset githubAsset) (UpdateResult, error) {
	cached, err := reusableUpdateStage(ctx, root, release, pkg)
	if err != nil {
		return UpdateResult{}, err
	}
	if cached != nil {
		result := updateResult(inspection, "staged", "")
		result.Version, result.AssetName, result.Stage = release.Version, pkg.AssetName, cached
		return result, nil
	}
	downloads, err := os.MkdirTemp(root, ".headroom-download-")
	if err != nil {
		return UpdateResult{}, err
	}
	defer os.RemoveAll(downloads)
	if err = os.Chmod(downloads, 0o700); err != nil {
		return UpdateResult{}, err
	}
	archive := filepath.Join(downloads, pkg.AssetName)
	file, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return UpdateResult{}, err
	}
	digest, size, downloadErr := c.download(ctx, asset.URL, pkg.Size, file)
	syncErr := file.Sync()
	closeErr := file.Close()
	if downloadErr != nil {
		return UpdateResult{}, downloadErr
	}
	if syncErr != nil {
		return UpdateResult{}, syncErr
	}
	if closeErr != nil {
		return UpdateResult{}, closeErr
	}
	if size != pkg.Size {
		return UpdateResult{}, errors.New("downloaded package size does not match release manifest")
	}
	if digest != pkg.SHA256 {
		return UpdateResult{}, errors.New("downloaded package checksum does not match release manifest")
	}
	if err = os.Chmod(archive, 0o400); err != nil {
		return UpdateResult{}, err
	}
	stage, err := StageArchive(archive, root, Expectations{Version: release.Version, Platform: inspection.Platform,
		Architecture: inspection.Architecture, AssetName: pkg.AssetName})
	if err != nil {
		return UpdateResult{}, err
	}
	// Persist the archive identity only after both outer and inner verification.
	// The archive itself can be discarded; reuse revalidates every staged file.
	stage.ArchiveSHA256, stage.ArchiveSize = digest, size
	if err = WriteJSON(filepath.Join(filepath.Dir(filepath.Dir(stage.PackageRoot)), "verified-stage.json"), stage); err != nil {
		os.RemoveAll(filepath.Dir(filepath.Dir(stage.PackageRoot)))
		return UpdateResult{}, err
	}
	if err = ctx.Err(); err != nil {
		os.RemoveAll(filepath.Dir(filepath.Dir(stage.PackageRoot)))
		return UpdateResult{}, err
	}
	result := updateResult(inspection, "staged", "")
	result.Version, result.AssetName, result.Stage = release.Version, pkg.AssetName, &stage
	return result, nil
}

func (c UpdateClient) download(ctx context.Context, rawURL string, limit int64, destination io.Writer) (string, int64, error) {
	response, cancel, err := c.open(ctx, rawURL, true)
	if err != nil {
		return "", 0, err
	}
	defer cancel()
	defer response.Body.Close()
	if response.ContentLength > limit {
		return "", 0, errors.New("release download is too large")
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(destination, hash), io.LimitReader(response.Body, limit+1))
	if err != nil {
		return "", written, err
	}
	if written > limit {
		return "", written, errors.New("release download is too large")
	}
	return hex.EncodeToString(hash.Sum(nil)), written, nil
}

func (c UpdateClient) fetch(ctx context.Context, rawURL string, limit int64, redirects bool) ([]byte, error) {
	if limit <= 0 || limit > MaxArchiveBytes {
		return nil, errors.New("download size limit is invalid")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New("release URL is invalid")
	}
	if redirects {
		if err = c.validateRedirectURL(parsed); err != nil {
			return nil, err
		}
	} else if !c.validAPIURL(parsed) {
		return nil, errors.New("release API URL is not allowed")
	}
	response, cancel, err := c.open(ctx, rawURL, redirects)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer response.Body.Close()
	if response.ContentLength > limit {
		return nil, errors.New("release download is too large")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("release download is too large")
	}
	return data, nil
}

func (c UpdateClient) open(ctx context.Context, rawURL string, redirects bool) (*http.Response, context.CancelFunc, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, func() {}, errors.New("release URL is invalid")
	}
	if redirects {
		if err = c.validateRedirectURL(parsed); err != nil {
			return nil, func() {}, err
		}
	} else if !c.validAPIURL(parsed) {
		return nil, func() {}, errors.New("release API URL is not allowed")
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 20 * time.Second}
	client := &http.Client{Transport: transport, Timeout: timeout}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !redirects || len(via) >= 5 {
			return http.ErrUseLastResponse
		}
		return c.validateRedirectURL(req.URL)
	}
	req, err := http.NewRequestWithContext(requestContext, http.MethodGet, parsed.String(), nil)
	if err != nil {
		cancel()
		return nil, func() {}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Headroom-package/"+SchemaVersionString())
	response, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, func() {}, fmt.Errorf("download release data: %w", err)
	}
	if response.StatusCode == http.StatusNotFound {
		response.Body.Close()
		cancel()
		return nil, func() {}, os.ErrNotExist
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		cancel()
		return nil, func() {}, fmt.Errorf("release server returned HTTP %d", response.StatusCode)
	}
	return response, cancel, nil
}

func (c UpdateClient) validAPIURL(candidate *url.URL) bool {
	if c.FixtureOrigin != nil {
		return sameOrigin(candidate, c.FixtureOrigin)
	}
	return candidate.Scheme == "https" && candidate.Hostname() == "api.github.com" && (candidate.Port() == "" || candidate.Port() == "443") &&
		repositoryReleasePath(candidate.EscapedPath(), "/repos/", "/releases/") && candidate.RawQuery == "" && candidate.User == nil
}

func (c UpdateClient) validateAssetURL(rawURL, version, name string) error {
	candidate, err := url.Parse(rawURL)
	if err != nil || candidate.User != nil || candidate.RawQuery != "" || candidate.Fragment != "" {
		return errors.New("release asset URL is invalid")
	}
	if c.FixtureOrigin != nil {
		if !sameOrigin(candidate, c.FixtureOrigin) {
			return errors.New("release asset origin is not allowed")
		}
		return nil
	}
	wantSuffix := "/releases/download/v" + version + "/" + name
	if candidate.Scheme != "https" || candidate.Host != "github.com" || !exactRepositoryReleasePath(candidate.Path, "/", wantSuffix) {
		return errors.New("release asset URL is not allowed")
	}
	return nil
}

func (c UpdateClient) validateRedirectURL(candidate *url.URL) error {
	if candidate.User != nil || candidate.Fragment != "" {
		return errors.New("release redirect URL is invalid")
	}
	if c.FixtureOrigin != nil {
		if sameOrigin(candidate, c.FixtureOrigin) {
			return nil
		}
		return errors.New("release redirect origin is not allowed")
	}
	allowed := map[string]bool{"github.com": true, "objects.githubusercontent.com": true,
		"release-assets.githubusercontent.com": true, "github-releases.githubusercontent.com": true}
	host := strings.ToLower(candidate.Hostname())
	if candidate.Scheme != "https" || (candidate.Port() != "" && candidate.Port() != "443") || !allowed[host] {
		return errors.New("release redirect origin is not allowed")
	}
	if host == "github.com" && !repositoryReleasePath(candidate.EscapedPath(), "/", "/releases/download/") {
		return errors.New("release redirect path is not allowed")
	}
	return nil
}

func repositoryReleasePath(path, prefix, suffix string) bool {
	for _, repository := range []string{canonicalRepository, legacyRepository} {
		if strings.HasPrefix(path, prefix+repository+suffix) {
			return true
		}
	}
	return false
}

func exactRepositoryReleasePath(path, prefix, suffix string) bool {
	for _, repository := range []string{canonicalRepository, legacyRepository} {
		if path == prefix+repository+suffix {
			return true
		}
	}
	return false
}

func sameOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host) && left.User == nil
}

func stableVersion(version string) bool {
	main := strings.SplitN(version, "+", 2)[0]
	return validVersion(version) && !strings.Contains(main, "-")
}

func (c UpdateClient) operationContext(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	return context.WithTimeout(parent, timeout)
}

// CompareVersions compares stable SemVer without integer conversion. Build
// metadata has no ordering significance, and arbitrarily long numeric fields
// remain safe.
func CompareVersions(left, right string) (int, error) {
	if !stableVersion(left) || !stableVersion(right) {
		return 0, errors.New("update version is not stable SemVer")
	}
	left = strings.SplitN(left, "+", 2)[0]
	right = strings.SplitN(right, "+", 2)[0]
	a, b := strings.Split(left, "."), strings.Split(right, ".")
	for i := 0; i < 3; i++ {
		if len(a[i]) != len(b[i]) {
			if len(a[i]) < len(b[i]) {
				return -1, nil
			}
			return 1, nil
		}
		if a[i] < b[i] {
			return -1, nil
		}
		if a[i] > b[i] {
			return 1, nil
		}
	}
	return 0, nil
}

func SchemaVersionString() string { return fmt.Sprintf("%d", SchemaVersion) }

// CleanupAbandoned removes only old, incomplete operation directories within a
// trusted install root. Verified stages are retained for the apply transaction.
func CleanupAbandoned(root string, olderThan time.Time) {
	removeOld := func(parent, pattern string, keepVerified bool) {
		// Never enumerate or remove through a replaced staging directory. The
		// caller's trusted root check does not cover this optional child.
		if err := validateInstallTargets(parent, ""); err != nil {
			return
		}
		entries, err := filepath.Glob(filepath.Join(parent, pattern))
		if err != nil {
			return
		}
		for _, entry := range entries {
			info, statErr := os.Lstat(entry)
			if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !info.ModTime().Before(olderThan) {
				continue
			}
			if keepVerified {
				if verified, verifyErr := os.Lstat(filepath.Join(entry, "verified-stage.json")); verifyErr == nil && verified.Mode().IsRegular() {
					continue
				}
			}
			_ = os.RemoveAll(entry)
		}
	}
	removeOld(root, ".headroom-download-*", false)
	removeOld(filepath.Join(root, "staging"), "package-*", true)
}
