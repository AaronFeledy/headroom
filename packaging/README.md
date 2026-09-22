# Headroom package contract

Official desktop packages use manifest schema 1 and exact versioned names:

- `Headroom-v<VERSION>-windows-x64.zip`
- `Headroom-v<VERSION>-windows-arm64.zip`
- `Headroom-v<VERSION>-linux-x86_64.tar.gz`
- `Headroom-v<VERSION>-macos-x86_64.tar.gz`
- `Headroom-v<VERSION>-macos-arm64.tar.gz`
- `Headroom-v<VERSION>-release.json` (Windows/Linux compatibility)
- `Headroom-v<VERSION>-release-all.json` (complete five-target matrix)

Official release `VERSION` is stable SemVer without a leading `v`; build
metadata such as `1.2.3+build.7` is preserved in the package, component and app
versions. Its three numeric components must each be at most 65534 so the same
derived `MAJOR.MINOR.PATCH.0` fits .NET assembly and Windows resource versions.
The full release manifest contains exactly the five desktop packages, their
byte sizes and SHA-256 digests. The original `release.json` keeps its exact
three-target Windows/Linux shape so previously installed managers can upgrade;
macOS installers and managers select `release-all.json`. Standalone `usage-server-*` and legacy `ClaudeUsageWidget-*` assets are
never desktop-package candidates.

CLI-only packages use the same strict schema with `package_kind: "cli"` and
`Headroom-CLI-v<VERSION>-<platform>-<architecture>` filenames. Their separate
`Headroom-CLI-v<VERSION>-release.json` contains all six native targets: Windows
x64/ARM64, Linux x86_64/ARM64, and macOS x86_64/ARM64. Desktop manifests omit the
new field, preserving compatibility with existing strict schema-1 readers.
The installed profile selects its catalog; a CLI update cannot acquire Qt or
silently convert into a desktop installation.

Every archive has one top-level directory matching its filename without the
archive suffix. It contains `package-manifest.json`, `bootstrap/`, and `bundle/`.
Devices, absolute paths, traversal, Windows device/ADS aliases, case-colliding
names, unlisted files and unsafe modes are forbidden. Windows and Linux
archives also forbid links. macOS permits only validated relative links inside
an individual packaged `.framework`; these preserve Apple’s native signing
structure. Link records, targets, resolution, extraction, and installed contents
are checked by the same Go validator; links cannot escape their framework. The inner
manifest lists the size and SHA-256 digest of every regular payload file.

Windows and Linux desktop bundles keep `headroom`, `headroom-cli`, `usage-server`, and the Windows-only
`headroom-credential-helper.exe` adjacent under `bundle/bin`. Qt libraries,
plugins and QML imports live under `bundle/lib`, `bundle/plugins`, and
`bundle/qml`; `bundle/bin/qt.conf` records those relative roots consistently
across Qt deployment-tool versions.
`QtQuick.Controls.Basic` is an explicit deployed dependency. Notices and exact
license texts are under `bundle/share` and are part of the hash inventory.
macOS keeps the GUI and server in
`bundle/Headroom.app/Contents/MacOS`, frameworks under `Contents/Frameworks`,
plugins under `Contents/PlugIns`, and QML under `Contents/Resources/qml`. The
runtime manager remains at `bundle/bin/headroom-package`; notices remain under
`bundle/share`. Native app bundles are ad-hoc signed and checked with
`codesign --verify --deep --strict`; Developer ID signing and Apple notarization
are not configured. Official packages also include
`bundle/share/licenses/qt/attributions/index.json`, generated from the five
hash-pinned Qt 6.8.3 source archives in `qt-sources-6.8.3.json`. The index is a
conservative module-source attribution inventory, records payload matches and
optional kit SPDX SBOMs, and does not present source or build-only records as
an exact binary SBOM. Missing archives, digest mismatches, malformed records or
missing referenced notices fail package assembly.

The bootstrap directory contains the shared Go package tool and the stable
launcher. Windows builds compile the launcher as a GUI-subsystem executable.
The additional `bootstrap/headroom-cli` is a console router compiled with
`main.invocationRole=cli`; its verified retained copy at
`bundle/bin/headroom-cli-launcher` supports entry migration. The public command
dispatches to `bundle/bin/headroom-cli` (inside `Headroom.app/Contents/MacOS` on
Mac). A CLI-only package instead uses `bundle/bin/headroom` as both its client
and server runtime, plus the shared manager, with no Qt dependency.
The bootstrap script verifies the release-manifest archive size and digest,
extracts only the exact package-tool entry into a private path, and delegates
all archive inspection, extraction and installation to that tool.

## Package tool interface

The portable command is `headroom-package` (`headroom-package.exe` on
Windows). Every command writes exactly one JSON object to standard output. A
successful command sets `ok` to `true`, names the `command`, includes its
typed `result`, and exits 0. A rejected command sets `ok` to `false`, includes
a bounded human-readable `error`, and exits 2. Diagnostics intended for a
person may also be written to standard error by the flag parser; callers must
make decisions from the exit status and JSON object rather than matching error
text.

- `inspect --archive <file>` validates bounded archive structure and returns
  `manifest` plus `archive_root`. It is cross-platform metadata inspection and
  does not establish payload hashes or permission to install. With
  `--install-root <path>` (or no option for the per-user default), `inspect`
  returns installation identity, completeness, active version, launcher and
  missing/corrupt paths.
- `verify --archive <file> [--version ... --platform ... --arch ... --asset
  ...]` privately extracts the package, verifies every recorded size, digest
  and mode, checks required runtime inventory and executable PE/ELF/Mach-O architectures,
  then removes the extraction. It can verify a foreign target for build and
  release tooling.
- `stage` accepts the same archive expectations plus `--install-root`. It
  requires the package OS and CPU to match the running host and extracts into
  an owned private `staging/package-<random>/contents/<archive-root>` path. The
  result records that `package_root`, package identity and manifest SHA-256;
  `verified-stage.json` beside `contents` records the same result. Failed
  validation removes its private stage.
- `install` adds `--entry-path` and `--cli-entry-path`, stages first, copies the complete bundle to
  a new immutable `versions/<VERSION>.generation-<manifest-prefix>-<random>`
  directory, writes its manifest and atomic `install-state.json`, and replaces
  the stable launcher only after validation. A private durable journal restores
  the prior state and every stable entry after interruption. Installing the
  active version again creates a separate generation, so external reinstall can
  repair a missing or corrupt component without replacing loaded files. If a
  prior recovery is blocked by a lost backup, a fully verified native reinstall
  may supersede only the journal that owns the current state, after stopping its
  exact recorded candidate; the old journal is retired only after the replacement
  state is durably complete.
- `check-update --install-root <path>` checks the public GitHub release metadata
  for a newer stable version of the exact native package. `stage-update` performs
  the same check, downloads the complete archive into a private directory,
  verifies its release size and SHA-256 plus the inner package contract, and
  writes a verified stage without changing the active version. Subsequent checks
  and staging requests reuse that stage only when its recorded archive size and
  SHA-256 match fresh release metadata and full package verification succeeds.
  Missing, damaged, older, or manually staged packages are downloaded again. `stage-repair`
  uses the installed version's exact release tag and is accepted only for a
  trusted installation with a missing server or credential helper. These
  commands accept `--cancel-stdin`; closing their input cancels the bounded
  operation and removes its incomplete private download or stage.
- `asset-name`, `create-package`, `create-release`, and `materialize-links`
  are build-recipe commands. `create-package` verifies its resulting archive;
  `create-release` fully verifies all five input archives before writing `release-all.json`.
  `create-release --legacy` verifies the three Windows/Linux archives and writes
  the compatibility `release.json`. Both files ship in every new release.
  `create-package --kind cli` and `create-release --kind cli` use the same archive
  validator and require the complete six-target CLI matrix.
- `prepare-apply` creates a private manager copy and a bounded request that pins
  the prior state digest, verified stage, current application process identity,
  and optional manager-owned server identity. The detached manager revalidates
  them under the install lock before acknowledging. Qt validates the returned
  transaction paths and acknowledgement, then writes a nonce-bound commit;
  without that second commit the transaction expires without changing the
  running installation. `apply` waits for the exact old processes, activates a
  new generation, and accepts readiness only from the expected executable,
  PID, nonce, and compiled package version. `recover` stops an exact recorded
  candidate before restoring an interrupted transaction. Apply outcomes are
  persisted for the reopened UI as `applied`, `rolled_back`, or
  `recovery_required`.

The stable launcher is `headroom.exe` at the Windows install root and
`headroom-launcher` at the Linux install root, copied to the user-facing Linux
entry path. Each launcher has an adjacent mode-0600 `<launcher>.root` file
containing one absolute clean install-root path. This association makes custom
roots work without environment variables. The launcher verifies
`install-state.json`, its canonical legacy `versions/<VERSION>` or immutable
generation path, manifest digest,
application and required runtime files before starting the Qt executable. A
missing `usage-server` or Windows credential helper keeps the verified package
identity trusted and permits the UI to open so a matching-version repair can
run; a missing or corrupt desktop/runtime blocks launch. It passes
`HEADROOM_INSTALL_ROOT`, `HEADROOM_LAUNCHER_PATH`, and
`HEADROOM_PACKAGE_VERSION` to Qt. `StartupService` registers that stable path,
so version changes do not rewrite login configuration.

Restart-to-apply keeps ordinary launch arguments and runtime configuration,
overrides stale `HEADROOM_*` metadata with the exact active generation, and
adds a private restart flag so a tray-started application reopens its popup.
Provider connectivity is not part of readiness. Qt reaches readiness after
configuration, primary-instance IPC, and the QML root are initialized. Only a
manager-owned bundled server is awaited during handoff; an attached local
server outside the managed install and remote services are never stopped by the
package manager. A standalone server registered by the same installed
`headroom serve` participates through a private receipt with its executable,
PID, kernel creation token, arguments, and recognized server configuration.
The manager also accounts for a Windows console wrapper before replacing it.

An external installer also verifies which immutable generation actually starts.
It launches the stable entry with a private readiness nonce and accepts only a
marker whose executable and compiled version match the newly selected install
state. A process already owning the normal settings scope is left running; in
that case the installer reports that the on-disk install succeeded and asks the
user to quit the old window and relaunch the stable entry. `--no-launch` and
`-NoLaunch` skip activation and print that restart instruction.

## Release JSON

Both release manifests use strict schema 1 JSON:

```json
{
  "schema": 1,
  "product": "Headroom",
  "version": "1.2.3",
  "packages": [{
    "platform": "windows",
    "architecture": "x86_64",
    "asset_name": "Headroom-v1.2.3-windows-x64.zip",
    "size": 123456,
    "sha256": "<64 lowercase hex characters>",
    "package_manifest_path": "Headroom-v1.2.3-windows-x64/package-manifest.json",
    "components": {
      "application": {"path": "bundle/bin/headroom.exe", "version": "1.2.3"},
      "server": {"path": "bundle/bin/usage-server.exe", "version": "1.2.3"},
      "credential_helper": {"path": "bundle/bin/headroom-credential-helper.exe", "version": "1.2.3"},
      "launcher": {"path": "bootstrap/headroom.exe", "version": "1.2.3"},
      "manager": {"path": "bootstrap/headroom-package.exe", "version": "1.2.3"}
    }
  }]
}
```

New release arrays contain exactly `windows/x86_64`, `windows/arm64`,
`linux/x86_64`, `macos/x86_64`, and `macos/arm64`. Readers also accept the
previous complete three-target release format so existing releases remain
usable. Linux and macOS use executable names without `.exe` and a JSON `null`
credential helper. Archive size is an integer from 1 byte through 2 GiB. The
inner package manifest adds the Qt version, runtime baseline and a sorted list
of every payload path, size, SHA-256 and Unix mode, with an optional `link_target`
for scoped macOS framework links. All component versions and
the release tag must equal the top-level strict SemVer.

Per-user installations use immutable version directories. Windows stores the
stable launcher, package tool, atomic state and versions under
`%LOCALAPPDATA%\Headroom`; shortcuts and startup target the stable
`headroom.exe`. Its embedded icon and version resources are compiled from the same icon asset and package version as the Qt desktop using
the pinned build-only `go-winres` compiler, then linked before package assembly,
manifest hashing, and signing. The Windows
installer explicitly uses that launcher as the Start menu shortcut's icon source
and refreshes the shortcut on install/reinstall. In-app updates replace the stable
launcher transactionally; the shortcut's target stays valid and its timestamp does
not need to change. Windows icon sizes are rendered directly from the shared SVG
with alpha preserved; regenerate the committed ICO with
`python3 packaging/generate-windows-icon.py` (CairoSVG and Pillow required).
Linux's per-user installer copies the packaged SVG to the stable
`$XDG_DATA_HOME/icons/hicolor/scalable/apps/headroom.svg` location (defaulting to
`~/.local/share`) and uses `Icon=headroom` in its desktop entry. Linux and Mac
installer tests verify that launcher identities and copied icons survive changes
to the active generation.

Linux stores state and versions under
`${XDG_DATA_HOME:-$HOME/.local/share}/headroom`; the desktop launcher remains
inside that root and `$HOME/.local/bin/headroom` becomes the CLI entry. macOS
keeps the Finder wrapper and adds the same CLI path. Windows uses
`<install-root>\cli\headroom.exe` for the public command. Settings stay in their
existing roaming/XDG locations and are never part of an application bundle.

`install.sh` accepts `--install-root`, `--entry-path`, `--cli-entry-path`, `--cli`,
`--no-launch`, and `--dry-run`. `install.ps1` accepts `-InstallRoot`, `-EntryPath`,
`-CLIEntryPath`, `-CLI`, `-NoLaunch`, and
PowerShell `-WhatIf`. Offline and CI installation supplies both an exact local
package and its exact release manifest with `--package` plus
`--release-manifest`, or `-PackagePath` plus `-ReleaseManifestPath`; neither file
is accepted alone. Online installation follows at most five HTTPS redirects and
allows only GitHub API, repository, and documented release-asset hosts on port
443. Every response is streamed into private bounded storage, and the release
manifest's exact size and digest are checked before the Go validator runs.

The portable Linux x86_64 archive is built on Ubuntu 22.04. It bundles Qt but
uses the baseline desktop's glibc, libstdc++, graphics, font, X11/XCB, Wayland,
D-Bus and OpenSSL 3 ABI libraries. The generic bundle uses X11, including
XWayland in a Wayland session, so the popup can be positioned beside the tray.
It falls back to native Wayland rendering when XWayland is unavailable; in that
case the compositor controls window placement. Explicit `QT_QPA_PLATFORM` and
Qt `-platform` overrides are preserved. On Ubuntu 22.04, install the runtime packages `libegl1`,
`libgl1`, `libglx0`, `libopengl0`, `libdrm2`, `libgbm1`, `libfontconfig1`,
`libfreetype6`, `libglib2.0-0`, `libgssapi-krb5-2`, `libssl3`,
`libwayland-client0`, `libwayland-cursor0`, `libwayland-egl1`, `libx11-6`,
`libx11-xcb1`, `libxkbcommon0`, `libxkbcommon-x11-0`, `libxcb1`,
`libxcb-cursor0`, `libxcb-glx0`, `libxcb-icccm4`, `libxcb-image0`,
`libxcb-keysyms1`, `libxcb-randr0`, `libxcb-render-util0`, `libxcb-render0`,
`libxcb-shape0`, `libxcb-shm0`, `libxcb-sync1`, `libxcb-xfixes0`,
`libxcb-xkb1`, `zlib1g`, and `libzstd1`. `libc6`, `libgcc-s1`, `libstdc++6`,
and `libdbus-1-3` are also part of the baseline and normally already installed
on an Ubuntu desktop. Native KDE Wayland tray attachment requires a distro/source
build with `HEADROOM_WITH_KDE_TRAY` and `HEADROOM_WITH_LAYER_SHELL`; those source
options remain enabled by default. Package smoke tests do not count as an
interactive tray-placement test.

Source and system-managed CMake installs remain direct installs and use their
own update method. They do not create package state or redirect startup through
the per-user launcher. Only a native, trusted per-user installation may use the
public acquisition commands. The desktop performs one delayed startup check,
automatically downloads and verifies a newer bundle, and advertises restart only
after the package tool returns a matching `verified-stage.json`. Capture and
explicit-config sessions do not make public update requests.

The public CLI calls the running desktop through its current-user private IPC
endpoint. Read-only snapshots expose usage without credentials; update requests
wait for the desktop's existing acknowledgement/commit transaction. Optional
`prepared_stage` data comes only from the local verified stage, and the manager
revalidates it before apply. A CLI update with no running desktop uses a CLI
readiness candidate and leaves the desktop closed.

Explicit Windows/WSL pairing uses a bounded native stdio protocol and reciprocal
private records. Both profiles stage the exact same version through the normal
release validator. The peer applies first; the initiator verifies its terminal
apply result using the retained immutable manager, avoiding a Windows bootstrap
file lock. Partial completion is persisted for exact-version recovery. Pairing
does not expose an HTTP update route or transfer provider credentials. See the
[CLI guide](../docs/cli.md) for setup and recovery.

Managed standalone servers register private process receipts. Linux/WSL also
supports the fixed per-user `headroom.service`: apply stops and restarts only that
unit after matching its MainPID to the exact recorded process. A replacement
acknowledges readiness after successful configuration and listener binding.
System-wide or unrelated units remain outside updater ownership.

`tests/cli_systemd_smoke.py` verifies a real Linux per-user service repair using
a disposable CLI package install and disabled providers. It requires a working
user bus and refuses an existing `headroom.service`. Its runtime-only unit is
stopped and removed afterward. Linux CLI jobs run this fixture on isolated CI
runner accounts; it does not enable a service on a user's machine.

## Reusable release recipe

`.github/workflows/headroom-packages.yml` accepts one validated version and
builds the same five desktop and six CLI packages for pull requests, main-branch releases
and explicit `v*` tags. It runs the native desktop, package, installer and
transaction gates before assembling a reviewable `headroom-release-assets`
artifact. That artifact contains the eleven packages, three release manifests,
`install.sh`, `install.ps1`, six standalone server binaries and `SHA256SUMS`.
The PR caller uses a version with build metadata so that path is exercised
without publishing. The release caller resolves a main-branch version in
dry-run mode, and the resolver and package jobs remain read-only. Only after the
native, server, Docker and installer gates pass does its write-scoped
publication job create or verify the tag at the initiating commit and publish
the aggregate artifact. The main run does not rely on the `GITHUB_TOKEN` tag
event to start another run. A separately pushed `v*` tag uses the identical
package and publication recipe. The publication tag is annotated with the
resolved changelog. A rerun detects one validated stable tag already pointing
to the exact initiating commit, reuses its version and message, and rejects an
ambiguous or differently targeted tag instead of incrementing again.
