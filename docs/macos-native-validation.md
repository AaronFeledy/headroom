# macOS native validation — 2026-09-13

Headroom v2.0.1 was installed through the public shell installer and exercised
in the existing Docker-OSX Intel VM. The native source build included the
startup attribution and settings-copy changes from this validation.

## Environment

- macOS 26.5.1, build 25F80, x86_64; four virtual CPUs and 4 GB RAM.
- Source baseline: `3226f3b` plus the accompanying local changes.
- Release runtime and source-build kit: official Qt 6.8.3.
- Apple Command Line Tools 26.6; bundled Python 3.9.6.
- Native source build used the installed macOS 15.4 SDK and retained the
  macOS 12 deployment target. The default macOS 26 SDK failed to link the
  older Qt kit because it no longer supplies AGL.

## Verified behavior

- The documented `curl .../main/install.sh | sh` flow selected the Intel
  v2.0.1 package, installed it under `~/Library/Application Support/Headroom`,
  and confirmed the exact running generation through the readiness handshake.
- The stable `~/Applications/Headroom.app` entry opened successfully. Finder
  displayed the purple Headroom H icon. A subsequent launch exposed the
  menu-bar issue described below; the fixed launcher restored the provider icon.
- The app started one adjacent `usage-server` child in Local mode. Its settings
  reported server version 2.0.1 and a reachable backend.
- Tray activation positioned the popup below the menu bar. The context menu
  contained Open Headroom, Refresh usage, Settings, and Quit Headroom.
- A confirmed menu Quit stopped both the GUI and its owned server; reopening
  the stable entry started a new GUI/server pair. Synthetic mouse input in this
  VM was inconsistent, so some click attempts required observation and retry.
- Start at login wrote an owner-private LaunchAgent pointing at the stable
  Finder launcher with `--background`. It did not bootstrap a service or
  replace the running GUI/server. Turning it off removed the entry.
- The installed CLI returned the desktop's JSON snapshot successfully.

Authenticated smoke checks passed for Claude, ChatGPT, Cursor, and Grok using
owner-private credential files. Repeated CLI snapshots confirmed successful
responses without reauthentication, nonempty unique meter IDs, the 12-meter
limit, finite utilization values, and null errors. Only provider status and
structural validation results were recorded; no credentials or account readings
were included in the evidence. The app remained in Local mode with one GUI and
one owned server. No reset-credit action was invoked.

Fixture data supplied the visual meter, warning, ordering, and offline-state
checks. Live provider windows were not captured. Real account notifications and
visual agreement with live readings were not validated.

## Tests and visual checks

- 13 existing Python Mac packaging/installer tests passed on Linux.
- The focused startup regression suite passed on Linux.
- `macos_package_smoke.py` passed on macOS against the released archive,
  including strict ad-hoc signature verification, Cocoa/offscreen captures,
  installation paths containing spaces, readiness identity, and the
  provider-disabled server/TLS smoke checks.
- `macos_finder_smoke.py` passed against the actual stable Finder entry.
- Ten native CTest suites passed: startup, update, remoteupdate, tray,
  trayattention, popup, displayplatform, settings, instance, and ui.
- The Cocoa UI tests `dragReordersAndDrivesTray`,
  `preservesAndDisplaysCustomInterval`, and `rendersOneTwoFourAndTwelveMeters`
  passed with the software renderer. Their captured normal, compact, settings,
  reordered, critical, and offline views were inspected.

Reset-action tests were not run. The broader Qt TLS-fixture suites were not
run in the user's login keychain: their disposable-keychain wrapper is
restricted to hosted CI. The separate packaged Go server/TLS smoke test did
run successfully. Apple Silicon and physical Mac hardware were not tested.

## Changes and remaining limits

The Mac settings now use menu-bar terminology and accurately describe startup
at the next login. The LaunchAgent includes `AssociatedBundleIdentifiers` for
Headroom's stable launcher and application, as Apple's local `launchd.plist`
manual recommends. Regression tests preserve recognition, migration, and
removal of the exact v2.0.1 entry format while rejecting altered entries.
The desktop build guide records the tested SDK selection for Qt 6.8.3 on
Command Line Tools 26.

Finder and tray icons worked, but System Settings/background-item notifications
continued to show a generic executable icon and lowercase `headroom` label
in this VM, including after ad-hoc signing the source test bundle. The added
association metadata is not claimed to have resolved that presentation issue.
No Developer ID signing or notarization was performed.

At completion, the published v2.0.1 app payload was running behind the locally
patched stable Finder launcher described below, with all four providers
authenticated. Startup was off and the source test build was stopped.
The rebuilt app remains at
`~/headroom-test/build/headroom.app`; logs, source, and native captures remain
under `~/headroom-test`. The local code changes were not published or installed
as a replacement managed release.

## Follow-up: missing menu-bar icon from the Finder launcher

A subsequent normal Finder launch exposed a gap in the initial checks: launching
`~/Applications/Headroom.app` left the GUI and local server running, but no
Headroom status item appeared. A menu-bar-only capture confirmed the absence.
Headroom was already enabled in macOS Tahoe's **Allow in the Menu Bar** settings.
Restarting the app, refreshing that setting, and restarting the current user's
Control Center did not restore it. Opening the installed payload bundle directly
through Finder did restore the icon.

The stable launcher previously used Unix `exec` to replace its process with the
Qt executable. Normal macOS GUI launches now ask AppKit Launch Services to open
the actual payload bundle. A fixed JXA bridge calls AppKit directly; arguments
and the environment travel as JSON over stdin rather than shell interpolation
or command-line environment assignments. The wrapper's inherited bundle/service
identity is omitted from the destination environment. Background launches also
request no activation and suppress Qt's forced foreground activation at startup.
CLI launches still use `exec`; the updater's direct launch and exact-process
readiness checks are unchanged.

The fix was built into the VM's stable launcher, with the original backed up at
`~/headroom-test/launcher-before-menu-bar-fix`. The immutable v2.0.1 payload was
not modified. A future reinstall of the currently published release can replace
this local launcher fix until a release includes the source change.

After the fix, the ordinary stable Finder entry displayed the selected provider
icon immediately left of Spotlight. Clicking it changed the popup from hidden
to visible below the menu bar, verified through native window geometry without
capturing account data. All four authenticated provider snapshots passed again,
with one GUI and one owned server.

Background startup was separately verified with the icon visible, the popup
hidden, and System Settings retaining foreground focus. Reopening the stable
Finder entry activated the existing instance without creating a second server.

Validation also covered the complete manager test/race suite and vet, Intel and
ARM64 Mac builds, Windows compilation, the isolated Finder capture smoke test, and the
installer readiness nonce/version/executable handshake. Reset-related tests and
actions remained excluded. Unit regressions cover argument preservation,
private environment transport, removal of inherited wrapper identity, empty
argument lists, background activation policy, and rejection of non-bundle GUI
targets.


The first PR CI run exposed an asynchronous-completion assumption in
`macos_package_smoke.py` on both Mac architectures: it inspected the image as
soon as the stable launcher returned. The harness now removes stale outputs and
waits up to 30 seconds for a complete PNG and its readiness file. Nonce, version,
and executable checks are retained. Regression tests cover delayed output,
partial images, missing readiness, and bounded failure; native CI validates the
updated harness against the exact PR packages.

Both native architectures then completed the Cocoa capture and exposed a second
issue: the explicitly offscreen Qt capture cannot use the AppKit launch lifecycle.
The launcher now executes explicit offscreen/minimal backends directly, preserving
their synchronous command-line behavior. Normal Cocoa GUI launches continue through
Launch Services. Routing regressions cover backend options and fallback lists;
launcher race tests, vet, both Mac cross-builds, and all ten macOS packaging tests
passed locally. Native CI remains the gate for the complete corrected packages.

The corrected PR packages subsequently passed the native Intel and ARM64 Mac
gates, including Cocoa and offscreen captures. The Linux manager race gate then
failed twice in managed-service stop recovery, in code unchanged by the Mac fix.
Linux could lose an exiting process's executable link before observing its zombie
state. The watcher now handles that transition like the existing Darwin watcher:
it retries within the wait deadline only when the independent kernel creation
identity still matches. Executable or creation-identity mismatches still fail
closed, and every signal still requires full executable/creation verification.
Focused exit, bounded-wait, and live-identity rejection tests passed 50 race-enabled
iterations locally. The complete manager race suite (with reset tests excluded),
vet, and build also passed after this correction. Native CI remains the final gate.
