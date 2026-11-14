#!/usr/bin/env bash
set -euo pipefail

# Invoke this helper through sudo so timeout and apt share an identity.
export DEBIAN_FRONTEND=noninteractive
apt_options=(
  -o Acquire::Retries=3
  -o Acquire::http::Timeout=30
  -o Acquire::https::Timeout=30
  -o DPkg::Lock::Timeout=60
  -o APT::Update::Error-Mode=any
)

# Internal phases run together under their outer timeout monitor.
case "${1:-}" in
  --download)
    shift
    apt-get "${apt_options[@]}" update
    exec apt-get "${apt_options[@]}" install -y --download-only "$@"
    ;;
  --install)
    shift
    exec apt-get "${apt_options[@]}" install -y --no-download "$@"
    ;;
esac

for attempt in 1 2; do
  if timeout --kill-after=15s 4m bash "$0" --download "$@"; then
    break
  else
    status=$?
    if [[ "$status" -ne 124 || "$attempt" -eq 2 ]]; then
      exit "$status"
    fi
  fi
done

# Package mutation is never retried, even after a graceful timeout.
timeout --kill-after=15s 5m bash "$0" --install "$@"
