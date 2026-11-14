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
  started=$SECONDS
  printf 'Headroom dependencies: phase=prepare attempt=%s event=start\n' "$attempt" >&2
  if timeout --kill-after=15s 4m bash "$0" --download "$@"; then
    printf 'Headroom dependencies: phase=prepare attempt=%s event=complete elapsed_seconds=%s\n' "$attempt" "$((SECONDS - started))" >&2
    break
  else
    status=$?
    printf 'Headroom dependencies: phase=prepare attempt=%s event=failed status=%s elapsed_seconds=%s\n' "$attempt" "$status" "$((SECONDS - started))" >&2
    if [[ "$status" -ne 124 || "$attempt" -eq 2 ]]; then
      exit "$status"
    fi
    printf 'Headroom dependencies: retrying download preparation after its deadline.\n' >&2
  fi
done

# Package mutation is never retried, even after a graceful timeout.
started=$SECONDS
printf 'Headroom dependencies: phase=install event=start\n' >&2
if timeout --kill-after=15s 5m bash "$0" --install "$@"; then
  printf 'Headroom dependencies: phase=install event=complete elapsed_seconds=%s\n' "$((SECONDS - started))" >&2
else
  status=$?
  printf 'Headroom dependencies: phase=install event=failed status=%s elapsed_seconds=%s\n' "$status" "$((SECONDS - started))" >&2
  exit "$status"
fi
