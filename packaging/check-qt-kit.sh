#!/usr/bin/env bash
# Required runtime plugins mirror headroom-manager/contract/manifest.go.
set -euo pipefail
: "${QT_ROOT_DIR:?install-qt-action must export QT_ROOT_DIR}"
: "${HEADROOM_QT_VERSION:?Qt version must be set}"
case "$1" in
  linux) files=(tls/libqopensslbackend.so imageformats/libqsvg.so iconengines/libqsvgicon.so) ;;
  windows) files=(tls/qschannelbackend.dll imageformats/qsvg.dll iconengines/qsvgicon.dll) ;;
  macos) files=(tls/libqsecuretransportbackend.dylib imageformats/libqsvg.dylib iconengines/libqsvgicon.dylib) ;;
  *) printf 'Unsupported Qt kit platform: %s\n' "$1" >&2; exit 1 ;;
esac
for file in "${files[@]}"; do
  if [[ ! -f "$QT_ROOT_DIR/plugins/$file" ]]; then
    printf '::error::Qt %s kit is missing required file: %s/plugins/%s\n' "$HEADROOM_QT_VERSION" "$QT_ROOT_DIR" "$file" >&2
    exit 1
  fi
done
