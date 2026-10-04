#!/usr/bin/env bash
set -euo pipefail
if [[ ${GITHUB_ACTIONS:-} != true || ${RUNNER_ENVIRONMENT:-} != github-hosted ]]; then
  printf 'Desktop preference changes are restricted to disposable hosted CI.\n' >&2
  exit 1
fi
previous=$(defaults read com.apple.universalaccess reduceMotion 2>/dev/null || true)
restore() {
  if [[ -n "$previous" ]]; then
    defaults write com.apple.universalaccess reduceMotion -bool "$previous"
  else
    defaults delete com.apple.universalaccess reduceMotion
  fi
}
if ! defaults write com.apple.universalaccess reduceMotion -bool true; then
  printf '::warning::macOS refused reduceMotion (hosted-runner TCC); native motion probe skipped.\n'
  exit 0
fi
trap restore EXIT
applied=false
for _attempt in 1 2 3 4 5; do
  if [[ $(swift -e 'import AppKit; print(NSWorkspace.shared.accessibilityDisplayShouldReduceMotion)' 2>/dev/null) == true ]]; then
    applied=true
    break
  fi
  sleep 1
done
if [[ "$applied" != true ]]; then
  printf '::warning::NSWorkspace did not observe reduceMotion (hosted-runner TCC); native motion probe skipped.\n'
  exit 0
fi
HEADROOM_EXPECT_PLATFORM_REDUCED_MOTION=1 QT_QPA_PLATFORM=cocoa QT_QUICK_BACKEND=software \
  "$1" platformReducedMotion
