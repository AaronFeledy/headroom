# Headroom desktop migration plan

Prepared from the repository on 2026-09-08 for [PR #10](https://github.com/AaronFeledy/headroom/pull/10). The PR starts with the working Linux Qt Quick client and the retained Windows WinForms client. This document is an implementation plan, not a claim that the work below is complete.

## Result and boundaries

Ship **Headroom**, one Qt Quick desktop application for Windows and Linux, using the existing Go usage server. Windows and Linux must run the same provider rendering, ordering, period calculations, warning state machines, diagnostics, and settings UI. Native integration belongs behind small platform adapters. macOS is not part of this migration.

Keep the current design: Dracula colors, frameless popup attached to the tray, usage meters above the sticky footer, drag handles below provider identity, centered provider logos without tray numbers, a two-line tray tooltip, and the first provider controlling the tray even when unavailable. Do not reintroduce the older Windows appearance or its independent severity thresholds.

Rename the product, primary source layout, build metadata, installers, documentation, and release assets to Headroom. Keep the GitHub repository URL unchanged. Keep the Go module/import paths, wire fields, provider keys such as `Codex`, server flags/environment variables, and `usage-server` executable name compatible. Display `Codex` as ChatGPT. Existing server configuration paths remain supported; cosmetic renaming must not disconnect deployed servers.

Support Windows x64 and ARM64, which are already release targets. The Linux desktop release initially targets x86_64, including this machine; do not imply that the existing Linux ARM64 server artifact is a tested desktop release. A Linux ARM64 desktop target may be added only with its own build and runtime evidence. Keep the source-build minimum at Qt 6.6 unless a required feature forces an explicit change. Use a reproducible Qt 6.8 patch release with MSVC 2022 for Windows CI, including its ARM64 kit. Qt documents Windows ARM64 support with MSVC 2022 in [Qt 6.8 platform support](https://doc.qt.io/qt-6.8/supported-platforms.html).

Keep `clients/windows/` as the explicitly labeled legacy client and regression reference until native Qt parity has been validated. New default downloads and installer launches must use the Qt UI. Do not delete the useful C# regression harness or quietly publish the legacy executable as a new Headroom Qt build.

## Inventory and acceptance map

| Capability | Current implementation | Required Headroom result |
| --- | --- | --- |
| Variable provider buckets, labels, subtitles, status-only billing, reauthentication guidance | Both clients | Shared Qt implementation preserves all supported server output |
| Drag ordering and primary tray provider | Both, with intentional selection differences | Preserve Headroom's stable first-provider behavior and imported Windows order |
| Pacing, hourly/daily/weekly notches, reset countdowns | New Qt implementation | Same code and tests on Windows and Linux |
| Warning tiers, hysteresis, transition alerts, tray severity | New Qt implementation | One shared policy and per-meter state; no separate Windows thresholds |
| Frameless tray popup, centered logos, concise tooltip, secondary warning dot | Qt, verified on KDE | Native Windows anchoring, activation, focus dismissal, DPI, and notification verification |
| Single instance and second-launch activation | Qt local socket/lock | User-scoped implementation works on Windows and Linux, including installed paths |
| Remote URL/token, prefix paths, bounded polling, stale data, retry backoff | Qt | Preserve behavior; endpoint changes cancel old work and never leak tokens |
| Start at login | Linux XDG only in Qt; Windows registry in legacy | Platform adapter, correct quoted executable, migrate existing startup preference without duplicate launches |
| Settings migration | Legacy PascalCase schema v3; Qt lower-case schema | Idempotent import with backup, explicit local/remote mode, no data loss |
| Debug console, app/server version | Qt | Shared UI and redacted event summaries on both platforms |
| Local server attach, bundled spawn, recovery, ownership | Legacy Windows only | Async managed-server service; Windows default local mode retained, remote mode never spawns |
| Cursor/Grok browser credential discovery and memory-only forwarding | Legacy Windows only | Retain supported Windows browsers and forwarding safety; Linux remains able to rely on server-side auth |
| Local server binary acquisition | Legacy Windows | Prefer bundled matching server; missing/mismatched installation has a tested repair or acquisition path |
| Automatic check, download, staging, restart update | Legacy Windows, executable-pair updater | Complete package-aware updater for official user installations; source/system installs use their documented update method |
| Matching client/server Windows packages | Legacy Windows exe assets | Qt runtime/QML/plugins, helper if used, matching server, version manifest, checksums, notices |
| Linux package/install/update | Local CMake installation only | Published runnable package and installer, preserving the current local installation/settings |
| CI | Legacy Windows build and Go jobs | Native shared Qt tests, native package smoke, legacy compatibility checks, release artifacts |

The old Windows implementation has known limitations that should not become requirements: fallback to another provider when the preferred provider fails, a busy tray tooltip, fixed utilization notifications, approximate quarter-month notches, and separate appearance logic are deliberately replaced by Headroom behavior.

## Architecture decisions for implementation

- Move the Qt source to `clients/desktop/`, leaving a small compatibility build wrapper or explicit redirect from `clients/linux/` while scripts and existing instructions migrate. Keep one QML module and one warning policy. New build directories must be ignored.
- Extract settings and platform services from the controller incrementally. The controller remains the owner of usage snapshots and warning states. Platform services report controlled status/error summaries rather than exposing tokens or raw process/network output to QML.
- Persist an explicit `local` or `remote` connection mode. On first Windows launch, legacy empty `ApiUrl` means local managed mode; nonempty means remote. On Linux, an existing empty configuration keeps the current setup flow and must not unexpectedly start a server. An explicit local option can use the same manager on Linux.
- Store settings beside the server config: Unix
  `${XDG_CONFIG_HOME:-$HOME/.config}/headroom/settings.json`, Windows
  `%APPDATA%\Headroom\settings.json`. Copy previous nested Headroom settings once
  when the new file is absent, keeping the source and the previous single-instance
  identity. Import `%APPDATA%/ClaudeUsageWidget/settings.json` only when neither
  Headroom file exists. Never overwrite the original or a malformed file during
  automatic import. `--config` remains isolated and must not import real settings.
- The new managed-server service uses asynchronous `QProcess` and injectable process/probe seams. Windows adds Job Object ownership; a process it only attaches to is never killed. Keep process ownership separate from reachability and provider health.
- Reuse the existing tested Windows browser discovery/snapshot/DPAPI/AES implementation through a small self-contained, headless C# helper, rather than rewriting browser cryptography in C++. The helper can link the existing reader source and narrow interfaces. It takes only a provider identifier, returns a bounded result through its private child-process pipe, and performs no networking. The Qt client sends credentials. Helper output is never forwarded into diagnostics. Fixture mode must not access real browser profiles. Native C++ replacement is acceptable only if it preserves equivalent tests and does not expand the task into new browser decryption techniques.
- Browser credential forwarding is Windows-specific parity, not a requirement to add Linux browser decryption. Linux local and remote modes use the Go server's supported credential discovery or explicitly configured credential sync. Preserve the current WSL deployment.
- Define one release manifest and asset naming scheme before implementing updates. The updater installs a complete versioned application bundle, not just `headroom.exe`; Qt DLLs, QML imports, plugins, helper, and matching server must stay together. Both installers and the updater consume the same verified package contract.
- Official per-user installations can offer automatic download/staging and a visible restart-to-apply action. Source-built or system-managed installations must be identified correctly and use their own update path rather than overwriting package-manager files. No platform requires administrator rights for the standard per-user install.

## Sequential implementation batches

Run one implementation agent at a time, using **GPT 5.6 Sol / medium** as requested. Each agent receives this plan, a bounded batch, the prior batch's findings, and file ownership for that turn. It implements, runs the relevant checks, and reports remaining failures. The coordinating agent reviews the diff, commits and pushes to PR #10, checks CI, and then starts the next agent. Agents do not merge, publish a release, change the running WSL backend, or rename the remote repository.

### 1. Shared desktop layout and early Windows build

Move `clients/linux` into the shared desktop layout without redesigning the UI. Adjust resources, source-directory test references, install paths, ignore rules, and compatibility entry points. Make KDE StatusNotifierItem and LayerShellQt detection Linux-only and individually disableable. Add the Windows GUI subsystem, Headroom executable metadata/icon/manifest, and explicit SVG dependency/deployment requirements. Keep Linux source install behavior intact.

Add an early PR CI job for Linux and native Windows x64 configuration, build, and shared tests so later batches receive real Windows compiler feedback. Gate OS-specific assertions rather than disabling whole test suites. Make startup test executables/paths and settings permission assertions platform-aware. Pin the chosen Qt/compiler setup and record the ARM64 toolchain arrangement.

Acceptance:

- A fresh build from `clients/desktop` passes existing six Qt test executables on Linux; no source still depends on the old build directory.
- A native Windows x64 job compiles the application and runs the portable tests, including an isolated disconnected `--screenshot` QML load.
- Both KDE-enabled local builds and a build with KDE integrations disabled succeed.
- Source install still produces `~/.local/bin/headroom`; the active user settings/order/token are untouched.

### 2. Settings migration and native desktop integration

Implement the settings service/schema and Windows import, including `ApiUrl`, `ApiToken`, refresh interval, notifications, `ProviderOrder`, legacy `PrimaryProvider`, and startup preference. Preserve unknown legacy data in its original/backup and explicitly map any setting that has no direct equivalent, such as always-available diagnostics replacing DebugMode. Keep migration idempotent, validate URL/token without logging them, and retain atomic writes. Use appropriate user-private Windows storage rather than claiming Unix mode bits enforce Windows ACLs.

Implement Windows startup registration under the current user's Run key, with Headroom name and quoted `--background` command. Migrate only the known legacy startup entry after the Headroom entry is successfully established. Correct user-scoped single-instance paths/names on Windows. Verify Qt tray popup behavior with Windows work areas and scaled coordinates; add a native adapter only for behavior Qt cannot provide reliably. Maintain settings/menu focus exceptions and fallback behavior on desktops without a tray.

Acceptance:

- Fixture migrations cover old schema versions, local and remote mode, Unicode/space paths, preserved token/order/interval/preferences, malformed JSON, missing files, existing Headroom config, repeat launch, and explicit `--config` isolation.
- Desktop migration copies Linux settings to the shared config folder, retaining
  the previous file and instance identity; it does not change server config files.
- Native Windows tests enable/disable a temporary isolated startup entry and verify quoting/error reporting without altering the user's actual startup setting.
- Single-instance activation and popup positioning/focus tests pass on Windows and Linux. The Windows manual smoke checks no title bar, no taskbar entry in tray mode, no console window, and usable multi-DPI placement.

### 3. Managed local-server lifecycle

Add local/remote selection to the shared settings UI and async server manager. Local mode probes `127.0.0.1:7823`, accepts `ok` or `degraded` health, attaches to an existing compatible server, or launches the bundled server with an explicit loopback listen flag. Keep existing server configuration discovery working. A configured token must be used by health and usage requests without putting it in command-line arguments. Report missing binaries/readiness failures clearly; connect package repair/acquisition to the release service once batch 6 exists.

Implement bounded readiness/restart behavior, cancellation on settings changes and shutdown, and Windows kill-on-job-close ownership. Ensure app exit and updater preparation stop only a child owned by Headroom. An occupied port, auth rejection, wrong endpoint, provider failure, or remote outage must not result in repeatedly spawning local servers. Never stop or replace the separately managed WSL service.

Acceptance:

- Unit/integration fixtures cover remote-no-probe/no-spawn, attach-no-kill, absent binary, spawn args, startup timeout, bounded restart exhaustion, settings switch races, shutdown, and owned versus unowned processes.
- Native Windows tests exercise Job Object cleanup; fake-only tests are insufficient for that seam.
- A packaged or staged test server can be launched and polled without provider credentials; degraded provider state is still a healthy process.
- Linux existing remote mode behaves exactly as before and never tries to own the network backend.

### 4. Browser credential forwarding and WSL request safety

Add the narrow Windows credential helper and Qt credential service. Retain Chrome/Edge/Brave Chromium and Firefox sources already supported by the legacy reader, using safe database snapshots including WAL handling and cleanup. Preserve existing limitations of browser encryption; do not bypass newer browser protections. Keep Cursor recovery conditional on the provider's authentication failure and Grok fallback conditional on missing CLI weekly data. Replace only the relevant provider response after a successful credential push and avoid replay loops.

Use the existing server credential endpoints and payload fields derived from `ApiClientWire` and `server/internal/api`. Allow browser credential submission only to a verified loopback URL or HTTPS remote URL; reject redirects for both bearer and provider credentials, including same-origin redirects for credential PUTs. All requests have bounded bodies/timeouts and are cancelled on endpoint change. No cookies, access tokens, headers, raw payloads, or child helper output enter diagnostics/settings/command lines.

Fix `server/deploy/wsl/sync_cursor_auth.py` to reject redirects rather than using urllib's default redirect handling, and validate its configured URL before credential access. Add a fixture redirect test demonstrating that neither bearer nor provider credentials reach the redirect target. Do not edit the live deployed helper or service in this batch.

Acceptance:

- Synthetic browser fixtures cover cookie selection, expiry, profile ordering, WAL snapshots, lock cleanup, malformed/unsupported encrypted values, and bounded helper failures without reading real credentials.
- HTTP fixtures verify loopback/HTTPS allow policy, remote plain-HTTP denial, same-origin/cross-origin redirect refusal, auth header isolation, cancellation, and provider replacement.
- Native Windows helper build/run and shared credential tests pass; existing browser snapshot harness remains green.
- WSL helper safety tests pass using temporary files and loopback fixtures only.

### 5. Complete platform packages and installer contract

Define version/OS/CPU/package fields, executable relative paths, bundled server version, optional helper, and hashes in a release manifest. Use stable exact Headroom asset names for Windows x64, Windows ARM64, and Linux x86_64. Keep standalone `usage-server-*` release assets and any explicitly labeled legacy artifacts separate. Do not let broad filename matching select the wrong platform, CPU, server-only asset, or legacy UI.

Create CMake/package scripts that stage the full Qt application. Windows includes QML/Quick Controls, image/icon, platform and TLS plugins, required runtime DLLs, matching Go server, and self-contained browser helper. Use Qt's QML-aware deployment tooling; [Qt's CMake deployment documentation](https://doc.qt.io/qt-6/cmake-deployment.html) distinguishes QML deployment from executable-only deployment. Linux packages need a documented runtime baseline and relocatable library/plugin paths; test the actual archive in a clean environment. Include optional KDE dependencies only when they can be deployed compatibly, and retain the source build for distro KDE integration. Bundle applicable dependency licenses/notices.

Provide PowerShell and Linux per-user installers that stage and validate a complete package before switching the active installation. Preserve the current `~/.local/bin/headroom` launch path and both platforms' settings. Migrate Windows shortcuts/install branding from Claude Usage Widget to Headroom. Match running processes by installation path and ownership; remove the old installer's broad `Stop-Process usage-server` behavior. Retain `-WhatIf` and piped PowerShell invocation support.

Acceptance:

- Archive content tests assert required executable, server, helper, QML/plugin/runtime, manifest and notice files for each platform/CPU.
- Windows x64 and ARM64 packages build with their respective Qt/MSVC/Go/helper architectures. Native ARM64 execution is required when a suitable runner/host is available; otherwise record build-only evidence explicitly and keep that remaining runtime check visible.
- Extracted Windows x64 and Linux x86_64 packages launch and render an isolated disconnected screenshot without a developer Qt installation or inherited Qt environment variables. Windows native QML/plugin loading must be exercised, not inferred from a Linux compile.
- Installer tests use temporary roots and synthetic packages for fresh install, upgrade, failed validation, missing components, paths with spaces, old shortcut migration, and dry run. No installer kills the unrelated backend.

### 6. Shared update checks, verified acquisition, and staging

Replace Linux-only `AppInfo` asset selection with the manifest contract. Keep server health and public release traffic isolated. Implement explicit updater states such as unavailable, checking, current, available, downloading, staged, and failed; wire status/actions into the existing settings layout. Add the legacy-equivalent automatic background release check for installed packages, with manual check and source/system-install behavior clearly distinguished. Match stable versions precisely and never offer a downgrade or a server-only/legacy package as a Headroom UI update.

Download to private temporary storage with size/time limits, verify the expected digest and package manifest, inspect archive paths before extraction, and stage the entire bundle. Reject absolute paths, traversal, unsafe links and incomplete/mismatched contents. Keep the running installation untouched on failure. Provide a repair/acquisition path for a missing bundled local server through the same release version contract; do not blindly download an arbitrary latest server into an older app bundle.

Acceptance:

- Fixture tests cover no release, rate limit, malformed metadata, prereleases, current/older versions, wrong CPU/OS, forbidden redirect/host, no bearer leakage, truncated/oversized download, bad checksum, traversal and partial bundle.
- Staging a complete synthetic version succeeds and preserves the current executable until apply.
- Source-built and system-managed installations do not falsely promise self-update or write outside their allowed installation mode.
- No tests download/install a real public release onto the user's machine.

### 7. Transactional update apply and recovery

Implement the final restart/apply step using a small separate updater or launcher that is not loading DLLs from the directory being replaced. On Windows, wait for the exact current process and owned child server to exit before switching the entire Qt bundle; do not use the legacy two-executable swap against a directory of loaded DLLs. On Linux, use the same versioned-bundle contract and stable user entry point. Never stop an attached or remote server.

Keep a known previous bundle and recover if file operations or relaunch fail. Install state must survive interruption between stage, stop, switch and launch. The updater must validate its stage/install roots and manifest again, consume only its own package, and avoid passing secrets to a shell. Preserve startup entries, shortcuts and settings across the switch. Clean abandoned staging directories only inside the application's own install/update root.

Acceptance:

- Failure-injection tests cover each switch operation, process wait timeout, locked DLLs, missing stage, interruption recovery, relaunch failure, rollback and success.
- A native Windows end-to-end test upgrades between two synthetic complete bundles and launches the new one; Linux has the same integration test.
- An unrelated process named `usage-server` survives installation and update tests.
- The app advertises restart-to-apply only when a complete verified stage exists; the update action closes/reopens the popup reliably.

### 8. Release automation, Headroom branding, and documentation

Consolidate release version injection so desktop, helper and bundled server use the same release version. Extend PR CI to build/package/test the supported desktop matrix and upload reviewable artifacts. Adapt both branch-generated and explicit-tag release paths without publishing duplicate or mismatched releases. Continue Go tests, race tests, vet, builds and Docker validation. Keep legacy C# harnesses during the transition.

Rewrite the root README around Headroom's new primary Windows/Linux UI and local/remote modes. Update client/server/deployment docs, installer copy, shortcuts/icons, metadata, diagnostics/about text, architecture diagrams, AGENTS instructions, parity audit and update guide. Remove stale statements that diagnostics or startup are unimplemented, that only Windows binaries ship, or that every Headroom client is remote-only. Explain legacy names retained for settings/server/API compatibility rather than doing an indiscriminate search-and-replace. Keep provider credential discovery locations accurate and Home Assistant documented as REST sensors.

Document supported desktop/CPU/runtime baselines, source builds, installing/migrating, updates, startup, settings/token handling, supported browser forwarding and its limitations, local-server ownership, logs, and troubleshooting. Replace the historical Linux-only parity audit with current cross-platform status while preserving useful design decisions. Keep the GitHub URL and existing service/config paths stable.

Acceptance:

- PR workflows build native Qt on Windows/Linux and publish both Windows architectures plus Linux review artifacts; release jobs package the same layout tested by PR CI.
- All download links, asset names, install commands, relative links and source paths match the implemented contract.
- Repository scan finds old branding only in intentional compatibility paths/legacy client/protocol references; visible new UI and primary docs say Headroom/ChatGPT.
- Release steps are validated with dry runs/artifacts; completing this batch does not publish a release or merge the PR.

### 9. Native end-to-end parity audit and final fixes

Use a fresh review of the implementation rather than only the plan checklist. Run the migrated legacy behavior scenarios against the new packaged application, fix discovered issues in bounded follow-up work, and update the PR around the final implementation. Validate the Linux package on this desktop and Windows package on a native host or runner. Interactive Windows validation may use WSL interop, but must use isolated configuration and synthetic/local fixture servers so an operator's real backend stays unchanged.

Acceptance:

- Native Windows and Linux: packaged launch, tray popup placement/toggle/outside dismissal, no title bar/buttons, settings/menu focus, centered icon/compact tooltip, DPI, drag order persistence, 1–12 buckets/status-only billing, period ticks, shared tier colors/alerts, startup, versions and diagnostics.
- Remote: reverse-proxy prefix, bearer auth rejection/recovery, provider auth failure, stale data/retry, endpoint switch, no unintended local server or credential forwarding over plain HTTP.
- Local: attach, spawn, provider-degraded readiness, restart limit, exit ownership, bundled credentials helper on Windows, and update while a child is owned versus a server attached.
- Migration and update: imported legacy Windows configuration, unchanged Linux preferences, both package architectures built, synthetic update success/rollback, and installation without the development SDK.
- Required Qt, Go, legacy lifecycle/bucket, installer and helper tests pass. No live account data, host-specific credentials or generated build artifacts are committed.
- PR description has a concise problem/result explanation, a supported-platform table, exact validation performed and any remaining platform-runtime limitations. The complete label is withheld if a required feature or primary native smoke still fails.

## Coordination and completion record

The coordinating agent owns commits/pushes, PR body updates, CI triage and final installation of the validated Linux result. It should record each batch's commit, tests, artifacts and remaining follow-up here or in the PR. Failed CI is part of the active batch; do not move on by describing untested code as complete. Native visual testing complements tests and does not replace them.

Batch 1 is verified. Local Qt 6.11.1 Linux validation passed fresh KDE-enabled and KDE-disabled builds, all six original test executables in both builds, the then-existing synthetic demo screenshot load, the `clients/linux` compatibility build, and a staged source install containing `bin/headroom`. Native CI run 34312021576 at commit `2529c75` passed the pinned Qt 6.8.3 Linux and Windows x64 builds, all six original suites including the Windows UI test, both historical demo screenshots, and the legacy build. That runtime demo surface was removed later. The workflow pins Qt 6.8.3 with MSVC 2022 on `windows-2022` and records the `win64_msvc2022_arm64` kit for the later packaging batch.

Batch 2 implementation was allowed to proceed independently while the final batch 1 runner was queued; that scheduling choice was not treated as evidence that batch 1 had passed. The implementation now has a shared settings schema with explicit connection mode, isolated Windows legacy import and create-once backups, current-user Windows startup registration and retryable legacy-entry migration, config-scoped single-instance activation, and expanded popup work-area fixtures. A fresh Qt 6.11.1 KDE-disabled Debug build in `clients/desktop/build-batch2` passed all eight test executables, including the new settings and instance suites. The local-socket tests require execution outside this environment's restricted network sandbox and passed there. An offscreen screenshot used the then-existing demo mode with an isolated configuration and did not create the settings file. Native CI run 34315273810 at commit `1f22df8` passed the pinned Qt 6.8.3 Windows x64 and Linux builds, all eight Qt suites and the historical demo captures, and the legacy compatibility checks. The runtime demo surface was removed later. Interactive Explorer tray, focus, taskbar, and multi-DPI checks remain assigned to batch 9; offscreen CI is not presented as interactive desktop evidence.

Batch 3 adds the explicit local/remote selector and asynchronous managed-server lifecycle. A fresh Qt 6.11.1 KDE-disabled Debug build in `clients/desktop/build-batch3` passed all nine test executables. Its synthetic process and HTTP fixtures cover remote isolation, compatible attach including degraded health, bounded spawn/readiness/restarts, token privacy, occupied or malformed endpoints, settings races, stale-request cancellation, and owned versus attached cleanup. An additional opt-in smoke launched a real staged Go `usage-server` with a temporary configuration and every provider disabled; it reached compatible readiness without provider credentials. Native Build run 34322070673 at `554aae6` passed Windows x64 and Linux, including all nine Qt suites, the historical demo rendering check, and the real Windows Job Object and abrupt-owner cleanup cases. The runtime demo surface was removed later. Server run 34322070723 also passed. Native Windows measurements showed approximately four seconds for a refused loopback connection, so the initial probe uses an eight-second budget separately from shorter readiness probes.

Batch 4 adds the shared recovery service, the narrow Windows helper, and redirect refusal in the repository's WSL sync script. A fresh `clients/desktop/build-batch4` build passed all ten Qt suites; the retained portable C# harness and three isolated Python HTTP tests also passed. The legacy Windows client and native fixture project compile, and helper publication is checked for both Windows architectures. Tests use generated data and temporary roots; ordinary Controller/UI tests explicitly disable production browser discovery. Native Build run 34324459441 at `ec3194b` passed Windows x64 and Linux, all ten Qt suites and the historical demo captures, the retained C# harness, both self-contained helper publications, and native DPAPI/AES fixtures. The runtime demo surface was removed later. Server run 34324459425 also passed. The helper preserves the legacy browser support limits, rejects unknown invocations before discovery, and communicates only through its parent process pipe. Automatic forwarding accepts HTTPS or numeric loopback HTTP; hostname `localhost` receives guidance to use `127.0.0.1` so DNS cannot change between validation and submission. No live credentials, profiles, deployed sync script, or backend service were used or changed during this batch.

Batch 5 defines manifest schema 1, exact versioned desktop assets, immutable per-user version roots, stable launchers, and a shared Go package validator for inspection, full verification, staging and installation. The package recipes use Qt 6.8.3 for official builds while retaining the Qt 6.6 source minimum, deploy the explicit Basic style dependency, and require Windows x64, native Windows ARM64, and Ubuntu 22.04 Linux artifacts. Separate clean jobs install only downloaded artifacts and exercise native and offscreen QPA paths without a configured Qt SDK. The Linux source build keeps optional KDE tray/layer-shell integration, while the generic archive supplies XCB and Wayland rendering without claiming KDE-specific popup attachment. Native Build run 34337007612 at `ef32c7c` passed all three package builds, native and offscreen clean-install smokes, Windows PowerShell 5.1 and piped `-WhatIf` installer fixtures, and release-manifest validation. Server run 34337007633 also passed.

Batch 6 adds a separate desktop update service and package-manager acquisition commands for trusted official installations. One delayed startup check automatically selects, downloads, verifies, and stages the exact native full bundle; manual checks and matching-version repair use the same validator and leave the active version unchanged. Public requests have an aggregate deadline, strict redirect and size rules, no backend bearer environment, graceful cancellation, and owned incomplete-file cleanup. The then-existing preview/demo paths, capture, explicit-config, source, and system-managed sessions remained isolated; the runtime preview/demo surface was removed later. Local validation in `/tmp/headroom-b6-qt-build` passed all eleven Qt suites, and the complete Go package-manager suite passed with loopback release fixtures. Native Build run 34343501749 at `8840465` passed all thirteen jobs, including Windows x64, native Windows ARM64 and Linux package builds, clean native/offscreen rendering, release metadata and installer fixtures. Server run 34343501759 also passed. The coordinating full manager race run reached Go's ten-minute test limit while decompressing copies of its own instrumented test executable; batch 7 replaces that payload with one small compiled native fixture and owns the full race rerun.

Batch 7 implements immutable physical generations, nonce-bound two-way restart handoff, exact native process identity, durable install/apply journals, readiness and rollback. The current application, optional manager-owned server, expected prior state hash and staged manifest are revalidated under one install lock before acknowledgement. Qt commits only after validating the complete private response and request; abandoned preparation expires without applying later. A candidate must report its actual executable, PID, compiled version and nonce after configuration, primary-instance IPC and QML initialization. Failure stops that exact candidate before verified rollback and reopening; unresolved recovery remains journaled and is never labeled successful. External reinstall can repair a corrupt app/runtime, while in-app apply requires an intact rollback app and allows missing auxiliary server/helper files. Local validation in `clients/desktop/build-batch7-agent` passed all eleven Qt suites. The official Go 1.25.14 manager suite and vet passed, and the full shuffled race suite passed in 591.139 seconds after replacing its oversized archive payload with one small compiled native fixture. Contract and manager test binaries also cross-compiled for Windows x64 and ARM64. Native Build run 34355742701 at `1c53c73` passed all thirteen jobs, including the full manager transaction suites on Windows x64 and ARM64, all eleven Qt suites on Windows and Linux, and all three clean package/installer paths. Server run 34355742806 passed Go tests, race, vet, build and the cache-only multi-architecture Docker build. A real Qt 6.8.3 same-version repair was also prepared, committed and reopened through the manager in an isolated root at `d3b0e34`; its result evidence remains task-local.

Batch 8 implementation consolidates PR and release packaging into one read-only reusable workflow with a stable SemVer input, full native package/installer gates, server test/race/vet/build and cache-only Docker gates, and one release artifact containing ten payload files plus `SHA256SUMS`. Main-branch version resolution is a dry run; only the final gated job may create or verify an annotated tag at the initiating commit. Exact-tag retry fixtures preserve the resolved version and changelog. Full SemVer including build metadata remains in product and package identity, while numeric PE/.NET versions are limited to 65534 per component. Official package assembly reads the actual Qt version and builds a hash-verified Qt 6.8.3 source-attribution inventory without claiming it is an exact binary SBOM. Local validation in `.omo/headroom-batch8/desktop-tests` built `1.2.3+build.7` with Qt 6.11.1 and passed all eleven Qt suites. Packaging/version/attribution/retry fixtures, focused manager contract tests, workflow lint, script parsing, installer streaming fixtures, metadata checks, links and branding scans passed. Native Build run 34360266420 passed 21 of 22 jobs, including all three packages, clean native/offscreen rendering, installer fixtures, and release aggregation. Its only failure was a 30 ms Windows process-start test budget, corrected in batch 9. The final verification result is maintained in [PR #10](https://github.com/AaronFeledy/headroom/pull/10); no tag or release was published during implementation.

Batch 9 closes the remaining bounded shared-UI and package-parity gaps. It preserves arbitrary valid polling intervals, renders measured Grok on-demand usage on its calendar-month pace, makes capture-mode meter geometry deterministic, shares the Dracula native palette with visual tests, applies status text as the reset-line override, chooses the first measurable tray bucket, avoids empty icon resource lookups, and treats KDE keyboard activation as an unknown anchor while preserving click-to-toggle. The rendered fixture covers 1, 2, 4, and 12 meters at wide and minimum widths and exercises real QML menus/settings focus on native QPAs. Linux-owned local servers receive a parent-death signal with a post-registration parent check; attached and unrelated processes remain untouched. Clean package jobs exercise the actual bundled server without providers or credentials, and Windows x64/ARM64 run the deployed credential helper against a generated SQLite browser fixture. External installers bind launch success to the exact selected generation readiness marker and give honest restart guidance when an existing primary remains active.

Final local validation built the optimized Qt 6.11.1 client with KDE StatusNotifierItem and LayerShellQt. All eleven suites passed; focused native Wayland tests exercised the real tray popup, drag ordering, menus, and custom interval preservation. Fixed-size rendered 1/2/4/12-meter geometry runs in the controlled offscreen renderer, while native QPA checks exercise interactions without assuming the compositor accepts arbitrary window sizes or synthetic hover. The new client is installed at the existing local entry, with every previously saved setting preserved and the schema migrated. A ten-second idle sample measured 159 MiB RSS and no measurable CPU use; this is a bounded sample, not a cross-platform benchmark. The downloaded Qt 6.8.3 production package at `72e4cde` also passed real same-version staging and restart into a distinct immutable generation, verifying actual nonce, PID, executable, compiled version, and unchanged isolated settings. Subsequent fixes concern test fixtures, CI isolation, and documentation.

The current native Windows x64/ARM64 and Linux results, CI artifacts, and remaining physical-desktop limitations are recorded in [PR #10](https://github.com/AaronFeledy/headroom/pull/10). Testing through the Windows ChatGPT app could not be dispatched because the Windows host was not connected to the session; native CI rendering and fixture execution are not presented as that physical-host test.

Review follow-up standardizes new Windows and Linux settings on Local mode while preserving saved remote overrides. Local discovery and attached HTTP requests send no saved bearer tokens or browser cookies. A bundled server uses a per-launch TLS identity and session token exchanged through private process pipes; all four authenticated request paths share certificate verification. This supersedes the earlier plan to send a configured bearer to discovered localhost HTTP endpoints. Independently managed authenticated servers use explicit Remote settings, and browser forwarding requires HTTPS.

The PR is complete when Headroom is the default new UI on Windows and Linux, existing users retain their connection/order/startup preferences, Windows managed-server and credential workflows are preserved, official packages install and update coherently, and native CI/package smoke demonstrates the claim. Keeping the legacy source for rollback does not prevent completion; shipping only a renamed Linux client or only a Windows cross-compile does.
