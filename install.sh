#!/bin/sh
set -eu

repo=AaronFeledy/headroom
package_path=
manifest_path=
platform=linux
architecture=x86_64
package_kind=
entry_path=
cli_entry_path=$HOME/.local/bin/headroom
case $(uname -s)/$(uname -m) in
  Linux/x86_64|Linux/amd64) ;;
  Linux/aarch64|Linux/arm64) architecture=arm64 ;;
  Darwin/arm64) platform=macos; architecture=arm64 ;;
  Darwin/x86_64) platform=macos ;;
  *) echo 'Headroom supports Linux x86_64/ARM64 and macOS Intel/Apple Silicon; Linux ARM64 requires --cli.' >&2; exit 1 ;;
esac
if [ "$platform" = macos ]; then
  install_root="$HOME/Library/Application Support/Headroom"
else
  install_root=${XDG_DATA_HOME:-"$HOME/.local/share"}/headroom
fi
no_launch=0
dry_run=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --package) package_path=$2; shift 2 ;;
    --release-manifest) manifest_path=$2; shift 2 ;;
    --install-root) install_root=$2; shift 2 ;;
    --entry-path) entry_path=$2; shift 2 ;;
    --cli-entry-path) cli_entry_path=$2; shift 2 ;;
    --cli) package_kind=cli; no_launch=1; shift ;;
    --no-launch) no_launch=1; shift ;;
    --dry-run) dry_run=1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
if [ "$platform/$architecture" = linux/arm64 ] && [ "$package_kind" != cli ]; then
  echo 'Linux ARM64 supports the CLI package; use --cli.' >&2; exit 1
fi
if [ -z "$entry_path" ]; then
  if [ "$package_kind" = cli ]; then entry_path=$cli_entry_path
  elif [ "$platform" = macos ]; then entry_path=$HOME/Applications/Headroom.app/Contents/MacOS/headroom
  else entry_path=$install_root/headroom-launcher
  fi
fi
if [ "$package_kind" = cli ]; then cli_entry_path=$entry_path; fi

if [ "$dry_run" -eq 1 ]; then
  printf 'Would validate and install Headroom at %s with application entry %s and CLI entry %s\n' "$install_root" "$entry_path" "$cli_entry_path"
  exit 0
fi
command -v curl >/dev/null && command -v python3 >/dev/null && command -v tar >/dev/null || { echo 'curl, python3, and tar are required.' >&2; exit 1; }
if { [ -n "$package_path" ] && [ -z "$manifest_path" ]; } || { [ -z "$package_path" ] && [ -n "$manifest_path" ]; }; then
  echo '--package and --release-manifest must be supplied together.' >&2
  exit 2
fi

private_root=$(mktemp -d "${TMPDIR:-/tmp}/.headroom-install.XXXXXXXX")
trap 'rm -rf -- "$private_root"' EXIT HUP INT TERM
release_json=$private_root/release.json
github_json=$private_root/github-release.json

download() {
  current=$1
  destination=$2
  maximum=${3:-8388608}
  timeout_seconds=${4:-600}
  response=$private_root/download-response
  headers=$private_root/download-headers
  curl_exit_file=$private_root/curl-exit
  deadline=$(( $(date +%s) + timeout_seconds ))
  redirects=0
  while [ "$redirects" -le 5 ]; do
    if ! python3 - "$current" <<'PY'
import sys, urllib.parse
u = urllib.parse.urlparse(sys.argv[1])
allowed = {'api.github.com', 'github.com', 'github-releases.githubusercontent.com', 'objects.githubusercontent.com', 'release-assets.githubusercontent.com'}
if u.scheme != 'https' or u.username or u.password or u.port not in (None,443) or (u.hostname or '').lower() not in allowed:
    raise SystemExit('refusing untrusted download URL: ' + sys.argv[1])
PY
    then
      return 1
    fi
    now=$(date +%s)
    remaining=$((deadline - now))
    [ "$remaining" -gt 0 ] || { echo 'download deadline exceeded' >&2; return 1; }
    connect_timeout=$remaining
    [ "$connect_timeout" -le 20 ] || connect_timeout=20
    rm -f -- "$response" "$headers" "$curl_exit_file"
    if (
      set +e
      curl --silent --show-error --proto '=https' --connect-timeout "$connect_timeout" --max-time "$remaining" \
        --user-agent Headroom-Installer --dump-header "$headers" --output - "$current"
      printf '%s\n' "$?" > "$curl_exit_file"
    ) | python3 -c '
import os, sys
destination, maximum = sys.argv[1], int(sys.argv[2])
written = 0
try:
    with open(destination, "xb") as output:
        while True:
            chunk = sys.stdin.buffer.read(min(65536, maximum - written + 1))
            if not chunk:
                break
            allowed = maximum - written
            if len(chunk) > allowed:
                if allowed:
                    output.write(chunk[:allowed])
                    written += allowed
                raise SystemExit(3)
            output.write(chunk)
            written += len(chunk)
except BaseException:
    try: os.remove(destination)
    except FileNotFoundError: pass
    raise
' "$response" "$maximum"; then
      stream_status=0
    else
      stream_status=$?
    fi
    curl_status=$(cat "$curl_exit_file" 2>/dev/null || printf '1')
    if [ "$stream_status" -ne 0 ]; then
      rm -f -- "$response"
      echo "download exceeds the $maximum byte limit" >&2
      return 1
    fi
    if [ "$curl_status" -ne 0 ]; then
      rm -f -- "$response"
      echo "download failed with curl exit $curl_status" >&2
      return 1
    fi
    status=$(awk 'toupper($1) ~ /^HTTP\// { code=$2 } END { print code }' "$headers")
    case "$status" in
      200) mv -- "$response" "$destination"; return ;;
      301|302|303|307|308)
        location=$(python3 - "$headers" "$current" <<'PY'
import sys, urllib.parse
lines=open(sys.argv[1], encoding='iso-8859-1').read().splitlines()
values=[line.split(':',1)[1].strip() for line in lines if line.lower().startswith('location:')]
if len(values)!=1: raise SystemExit('redirect location is missing or ambiguous')
print(urllib.parse.urljoin(sys.argv[2], values[0]))
PY
)
        current=$location; redirects=$((redirects+1)) ;;
      *) echo "download failed with HTTP $status" >&2; exit 1 ;;
    esac
  done
  echo 'too many download redirects' >&2
  exit 1
}

if [ "${HEADROOM_INSTALLER_SOURCE_ONLY:-0}" -eq 1 ]; then
  return 0 2>/dev/null || exit 0
fi

if [ -n "$package_path" ]; then
  [ "$(wc -c < "$manifest_path")" -le 4194304 ] || { echo 'release manifest is too large' >&2; exit 1; }
  cp -- "$manifest_path" "$release_json"
else
  download "https://api.github.com/repos/$repo/releases/latest" "$github_json"
  release_url=$(python3 - "$github_json" "$platform" "$package_kind" <<'PY'
import json, re, sys
d=json.load(open(sys.argv[1], encoding='utf-8'))
m=re.fullmatch(r'v(.+)', str(d.get('tag_name','')))
if not m: raise SystemExit('latest release tag is not a Headroom version tag')
prefix='Headroom-CLI' if sys.argv[3]=='cli' else 'Headroom'
suffix='release-all.json' if sys.argv[2]=='macos' and sys.argv[3]!='cli' else 'release.json'
n=f'{prefix}-v{m.group(1)}-{suffix}'
a=[x for x in d.get('assets',[]) if x.get('name') == n]
if len(a)!=1: raise SystemExit('release manifest asset is missing or ambiguous')
print(a[0]['browser_download_url'])
PY
)
  download "$release_url" "$release_json" 4194304
fi
[ "$(wc -c < "$release_json")" -le 4194304 ] || { echo 'release manifest is too large' >&2; exit 1; }
metadata=$(python3 - "$release_json" "$platform" "$architecture" "$package_kind" <<'PY'
import json, re, sys
d=json.load(open(sys.argv[1], encoding='utf-8'))
semver=r'(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?'
v=str(d.get('version',''))
prerelease=v.split('+',1)[0].split('-',1)
bad_numeric_prerelease=len(prerelease)==2 and any(x.isdigit() and len(x)>1 and x.startswith('0') for x in prerelease[1].split('.'))
if d.get('schema') != 1 or d.get('product') != 'Headroom' or not re.fullmatch(semver,v) or bad_numeric_prerelease: raise SystemExit('unrecognized release manifest')
platform, architecture, kind = sys.argv[2:]
if d.get('package_kind','') != kind: raise SystemExit('release package kind does not match the selected installation')
prefix='Headroom-CLI' if kind=='cli' else 'Headroom'
n=f'{prefix}-v{v}-{platform}-{architecture}.tar.gz'
p=[x for x in d.get('packages',[]) if x.get('platform')==platform and x.get('architecture')==architecture and x.get('asset_name')==n]
if len(p)!=1 or type(p[0].get('size')) is not int or not (0 < p[0]['size'] <= 2147483648) or not re.fullmatch(r'[0-9a-f]{64}',str(p[0].get('sha256',''))): raise SystemExit('Native package metadata is missing or invalid')
print(v); print(n); print(p[0]['size']); print(p[0]['sha256'])
PY
)
version=$(printf '%s\n' "$metadata" | sed -n '1p')
asset_name=$(printf '%s\n' "$metadata" | sed -n '2p')
expected_size=$(printf '%s\n' "$metadata" | sed -n '3p')
expected_hash=$(printf '%s\n' "$metadata" | sed -n '4p')
archive=$private_root/$asset_name
if [ -n "$package_path" ]; then
  [ "$(wc -c < "$package_path")" -eq "$expected_size" ] || { echo 'package size does not match release manifest' >&2; exit 1; }
  cp -- "$package_path" "$archive"
else
  package_url=$(python3 - "$github_json" "$asset_name" <<'PY'
import json, sys
d=json.load(open(sys.argv[1], encoding='utf-8')); a=[x for x in d.get('assets',[]) if x.get('name')==sys.argv[2]]
if len(a)!=1: raise SystemExit('package asset is missing or ambiguous')
print(a[0]['browser_download_url'])
PY
)
  download "$package_url" "$archive" "$expected_size"
fi
[ "$(wc -c < "$archive")" -eq "$expected_size" ] || { echo 'package size does not match release manifest' >&2; exit 1; }
actual_hash=$(python3 - "$archive" <<'PY'
import hashlib, sys
with open(sys.argv[1], 'rb') as source:
    digest=hashlib.sha256()
    for chunk in iter(lambda: source.read(1048576), b''): digest.update(chunk)
print(digest.hexdigest())
PY
)
[ "$actual_hash" = "$expected_hash" ] || { echo 'package hash does not match release manifest' >&2; exit 1; }

root_name=${asset_name%.tar.gz}
manager_entry=$root_name/bootstrap/headroom-package
manager=$private_root/headroom-package
# Only bootstrap the exact bounded regular entry after checking archive digest.
# Full archive validation and extraction belong exclusively to the Go manager.
python3 - "$archive" "$manager_entry" "$manager" <<'PY'
import sys, tarfile
with tarfile.open(sys.argv[1], 'r:gz') as archive:
    candidates=[entry for entry in archive if entry.name == sys.argv[2]]
    if len(candidates) != 1 or not candidates[0].isfile() or not 0 < candidates[0].size <= 67108864:
        raise SystemExit('package bootstrap entry is missing, ambiguous, or unsafe')
    entry=candidates[0]
    with archive.extractfile(entry) as source, open(sys.argv[3], 'xb') as output:
        remaining=entry.size
        while remaining:
            chunk=source.read(min(65536, remaining))
            if not chunk: raise SystemExit('package bootstrap entry is truncated')
            output.write(chunk); remaining-=len(chunk)
PY
chmod 0700 "$manager"
macos_entry() {
  python3 - "$entry_path" "$install_root" "$1" "${version_path:-}" <<'PY'
import os, pathlib, plistlib, stat, sys, tempfile
entry=pathlib.Path(sys.argv[1])
if not (entry.name == 'headroom' and entry.parent.name == 'MacOS' and entry.parent.parent.name == 'Contents' and entry.parent.parent.parent.suffix == '.app'):
    raise SystemExit(0)  # A custom command-line entry needs no Finder wrapper.
contents=entry.parent.parent
resources=contents/'Resources'
info_path=contents/'Info.plist'
for path in (contents.parent, contents, resources, info_path, resources/'headroom.icns'):
    if path.is_symlink(): raise SystemExit('refusing a linked Mac launcher bundle entry')
    if path.exists():
        mode=path.stat()
        if mode.st_uid != os.getuid(): raise SystemExit('Mac launcher bundle entry belongs to another user')
        if path in (contents.parent, contents, resources) and not stat.S_ISDIR(mode.st_mode):
            raise SystemExit('Mac launcher bundle directory is invalid')
        if path in (info_path, resources/'headroom.icns') and not stat.S_ISREG(mode.st_mode):
            raise SystemExit('Mac launcher bundle file is invalid')
if info_path.exists():
    if info_path.stat().st_size > 65536: raise SystemExit('Mac launcher metadata is oversized')
    with info_path.open('rb') as source: previous=plistlib.load(source)
    if previous.get('CFBundleIdentifier') != 'io.headroom.launcher':
        raise SystemExit('Headroom will not overwrite another Mac application')
if sys.argv[3] == 'check': raise SystemExit(0)
payload=pathlib.Path(sys.argv[2])/sys.argv[4]/'Headroom.app'/'Contents'
# This wrapper is the stable launcher, not a separately versioned product.
info={'CFBundleExecutable':'headroom', 'CFBundleIdentifier':'io.headroom.launcher',
      'CFBundleName':'Headroom', 'CFBundleDisplayName':'Headroom',
      'CFBundlePackageType':'APPL', 'CFBundleIconFile':'headroom.icns',
      'LSUIElement':True, 'LSMinimumSystemVersion':'14.4'}
resources.mkdir(exist_ok=True)
def write_atomic(path, data):
    fd, name=tempfile.mkstemp(prefix='.headroom-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as output:
            output.write(data); output.flush(); os.fsync(output.fileno())
        os.chmod(name, 0o644)
        os.replace(name, path)
    finally:
        if os.path.exists(name): os.unlink(name)
write_atomic(resources/'headroom.icns', (payload/'Resources'/'headroom.icns').read_bytes())
write_atomic(info_path, plistlib.dumps(info))
PY
}
linux_entry() {
applications=${XDG_DATA_HOME:-"$HOME/.local/share"}/applications
icon_theme=${XDG_DATA_HOME:-"$HOME/.local/share"}/icons/hicolor
icons=$icon_theme/scalable/apps
mkdir -p -- "$applications" "$icons"
# Copy from the verified generation into the user's stable icon theme location.
python3 - "$install_root" "$private_root/headroom.svg" "$version_path" <<'PY'
import pathlib, sys
root=pathlib.Path(sys.argv[1])
source=root/sys.argv[3]/'share/icons/hicolor/scalable/apps/headroom.svg'
pathlib.Path(sys.argv[2]).write_bytes(source.read_bytes())
PY
install -m 0644 "$private_root/headroom.svg" "$icons/headroom.svg"
desktop_tmp=$private_root/headroom.desktop
desktop_exec=$(python3 - "$entry_path" <<'PY'
import re, sys
print('"' + re.sub(r'([\\"`$])', r'\\\1', sys.argv[1]) + '"')
PY
)
cat > "$desktop_tmp" <<EOF
[Desktop Entry]
Type=Application
Name=Headroom
Comment=Usage monitor
Exec=$desktop_exec
Icon=headroom
StartupWMClass=Headroom
Terminal=false
Categories=Utility;
EOF
install -m 0644 "$desktop_tmp" "$applications/headroom.desktop"
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
  gtk-update-icon-cache -f -t "$icon_theme" >/dev/null 2>&1 || :
fi
}
manager_request() {
  response=$private_root/$1.json
  status=0
  "$manager" "$@" > "$response" || status=$?
  python3 - "$response" "$1" "$status" <<'PY'
import json, sys
with open(sys.argv[1], encoding='utf-8') as source:
    response=json.load(source)
if sys.argv[3] != '0' or response.get('ok') is not True:
    raise SystemExit(response.get('error') or 'Headroom package operation did not complete.')
if response.get('command') != sys.argv[2] or not isinstance(response.get('result'), dict):
    raise SystemExit('Headroom package manager returned an invalid result.')
PY
}
if [ "$platform" = macos ] && [ "$package_kind" != cli ]; then macos_entry check; fi
set -- install --archive "$archive" --install-root "$install_root" --entry-path "$entry_path" \
  --cli-entry-path "$cli_entry_path" --version "$version" --platform "$platform" --arch "$architecture" --asset "$asset_name"
if [ "$package_kind" != cli ] && [ "$no_launch" -eq 0 ]; then set -- "$@" --coordinate; fi
printf 'Installing Headroom %s...\n' "$version"
manager_request "$@"
version_path=$(python3 - "$private_root/install.json" "$version" <<'PY'
import json, sys
with open(sys.argv[1], encoding='utf-8') as source:
    state=json.load(source)['result']['state']
if state['active_version'] != sys.argv[2] or not state['version_path']:
    raise SystemExit('The installed generation does not match the requested package.')
print(state['version_path'])
PY
)

if [ "$package_kind" = cli ]; then
  printf 'Installed Headroom %s at %s\nRun %s for usage, %s serve for the server, or %s update to update.\n' "$version" "$install_root" "$cli_entry_path" "$cli_entry_path" "$cli_entry_path"
  exit 0
fi

if [ "$platform" = macos ]; then
  macos_entry write
else
  linux_entry
fi
activation_status=$(python3 - "$private_root/install.json" <<'PY'
import json, sys
with open(sys.argv[1], encoding='utf-8') as source:
    activation=json.load(source)['result']['activation']
print(activation['status'] if activation is not None else '')
PY
)
if [ -z "$activation_status" ]; then
  set -- activate --install-root "$install_root" --version-path "$version_path"
  if [ "$no_launch" -eq 1 ]; then set -- "$@" --no-launch; fi
  manager_request "$@"
  activation_status=$(python3 - "$private_root/activate.json" "$version_path" <<'PY'
import json, sys
with open(sys.argv[1], encoding='utf-8') as source:
    activation=json.load(source)['result']
if activation['version_path'] != sys.argv[2]:
    raise SystemExit('Activation refers to a different installed generation.')
print(activation['status'])
PY
)
fi
case "$activation_status" in
  ready|skipped|restart_required|unconfirmed) ;;
  *) echo 'Headroom package manager returned an unrecognized activation outcome.' >&2; exit 1 ;;
esac
printf 'Installed Headroom %s at %s\n' "$version" "$install_root"
case "$activation_status" in
  ready) printf 'Headroom %s is running.\n' "$version" ;;
  skipped) printf 'Launch skipped. Open %s when you are ready; quit any older Headroom window first.\n' "$entry_path" ;;
  restart_required) printf 'An earlier Headroom generation is still running. Quit it, then open %s.\n' "$entry_path" >&2 ;;
  unconfirmed) printf 'The package is installed, but startup could not be confirmed. Open %s and check Headroom diagnostics if it does not start.\n' "$entry_path" >&2 ;;
esac
