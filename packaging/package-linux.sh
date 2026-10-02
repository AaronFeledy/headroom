#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 11 ]]; then
  echo "usage: package-linux.sh VERSION BUILD_DIR WORK_DIR OUTPUT_DIR SERVER LAUNCHER MANAGER QT_ROOT QT_SOURCE_CACHE CLI CLI_LAUNCHER" >&2
  exit 2
fi

version=$1
build_dir=$2
work_dir=$3
output_dir=$4
server=$5
launcher=$6
manager=$7
qt_root=$8
qt_source_cache=$9
cli=${10}
cli_launcher=${11}
"$manager" asset-name --version "$version" --platform linux --arch x86_64 >/dev/null
asset="Headroom-v${version}-linux-x86_64.tar.gz"
package_root="${work_dir}/Headroom-v${version}-linux-x86_64"
[[ "$work_dir" = /* && "$output_dir" = /* && "$package_root" != "/" ]] || { echo "work and output directories must be absolute" >&2; exit 2; }

rm -rf -- "$package_root"
mkdir -p -- "$package_root/bundle" "$package_root/bootstrap" "$output_dir"
cmake --install "$build_dir" --prefix "$package_root/bundle"
build_metadata=$build_dir/headroom-build-metadata.json
readarray -t build_values < <(python3 - "$build_metadata" "$version" <<'PY'
import json, re, sys
d=json.load(open(sys.argv[1], encoding='utf-8'))
if d.get('product') != 'Headroom' or d.get('version') != sys.argv[2]: raise SystemExit('desktop build version does not match package version')
q=str(d.get('qt_version',''))
if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+', q): raise SystemExit('desktop Qt version is invalid')
print(q)
PY
)
qt_version=${build_values[0]}
for directory in plugins qml; do
  if [[ ! -d "$package_root/bundle/$directory" && -d "$package_root/bundle/lib/qt6/$directory" ]]; then
    mv -- "$package_root/bundle/lib/qt6/$directory" "$package_root/bundle/$directory"
  fi
  [[ -d "$package_root/bundle/$directory" ]] || { echo "deployed Qt $directory directory is missing" >&2; exit 1; }
done
cat > "$package_root/bundle/bin/qt.conf" <<'EOF'
[Paths]
Prefix = ..
Plugins = plugins
QmlImports = qml
EOF
copy_platform_plugin() {
  local filename=$1
  local source=
  for candidate in "$qt_root/plugins/platforms/$filename" "$qt_root/lib/qt6/plugins/platforms/$filename"; do
    if [[ -f "$candidate" ]]; then source=$candidate; break; fi
  done
  [[ -n "$source" ]] || { echo "Qt platform plugin $filename was not found under $qt_root" >&2; exit 1; }
  install -Dm0755 "$source" "$package_root/bundle/plugins/platforms/$filename"
  patchelf --set-rpath '$ORIGIN/../../lib' "$package_root/bundle/plugins/platforms/$filename"
}
copy_platform_plugin libqxcb.so
copy_platform_plugin libqoffscreen.so
if [[ -f "$qt_root/plugins/platforms/libqwayland.so" || -f "$qt_root/lib/qt6/plugins/platforms/libqwayland.so" ]]; then
  copy_platform_plugin libqwayland.so
else
  copy_platform_plugin libqwayland-generic.so
fi
install -m 0755 "$server" "$package_root/bundle/bin/usage-server"
install -m 0755 "$launcher" "$package_root/bootstrap/headroom"
install -m 0755 "$manager" "$package_root/bootstrap/headroom-package"
install -m 0755 "$manager" "$package_root/bundle/bin/headroom-package"
install -m 0755 "$cli" "$package_root/bundle/bin/headroom-cli"
install -m 0755 "$cli_launcher" "$package_root/bootstrap/headroom-cli"
install -m 0755 "$cli_launcher" "$package_root/bundle/bin/headroom-cli-launcher"
install -m 0644 packaging/THIRD_PARTY_NOTICES.txt "$package_root/bundle/share/headroom/THIRD_PARTY_NOTICES.txt"
install -Dm0644 LICENSE "$package_root/bundle/share/licenses/headroom/LICENSE"
mkdir -p "$package_root/bundle/share/licenses/qt" "$package_root/bundle/share/licenses/go/runtime" "$package_root/bundle/share/licenses/go/protobuf" "$package_root/bundle/share/licenses/go/yaml" "$package_root/bundle/share/licenses/openssl"
find packaging/licenses/qt -maxdepth 1 -type f -exec install -Dm0644 '{}' "$package_root/bundle/share/licenses/qt/" \;
mapfile -d '' qt_licenses < <(find "$qt_root/LICENSES" "$qt_root/licenses" -type f \( -name 'LICENSE*' -o -name '*NOTICE*' -o -name '*.txt' \) -print0 2>/dev/null || true)
for license in "${qt_licenses[@]}"; do relative=${license#"$qt_root"/}; install -Dm0644 "$license" "$package_root/bundle/share/licenses/qt/$relative"; done
[[ -s "$package_root/bundle/share/licenses/qt/LGPL-3.0-only.txt" && -s "$package_root/bundle/share/licenses/qt/Qt-GPL-exception-1.0.txt" ]] || { echo 'required Qt license texts are missing' >&2; exit 1; }
python3 packaging/qt_attributions.py package --source-cache "$qt_source_cache" --qt-root "$qt_root" \
  --payload-root "$package_root/bundle" --output "$package_root/bundle/share/licenses/qt/attributions" \
  --modules qtbase qtdeclarative qtshadertools qtsvg qtwayland
python3 - "$package_root/bundle/share/licenses/qt/attributions/index.json" "$qt_version" <<'PY'
import json, sys
d=json.load(open(sys.argv[1], encoding='utf-8'))
if d.get('qt_version') != sys.argv[2]: raise SystemExit('Qt attribution version does not match deployed Qt')
PY
install -m 0644 "$(go env GOROOT)/LICENSE" "$package_root/bundle/share/licenses/go/runtime/LICENSE"
module_cache=$(go env GOMODCACHE)
install -m 0644 "$module_cache/google.golang.org/protobuf@v1.36.11/LICENSE" "$package_root/bundle/share/licenses/go/protobuf/LICENSE"
install -m 0644 "$module_cache/gopkg.in/yaml.v3@v3.0.1/LICENSE" "$package_root/bundle/share/licenses/go/yaml/LICENSE"
install -m 0644 "$module_cache/gopkg.in/yaml.v3@v3.0.1/NOTICE" "$package_root/bundle/share/licenses/go/yaml/NOTICE"
for module in sys term; do
  module_dir=$(cd packaging/headroom-manager && go list -m -f '{{.Dir}}' "golang.org/x/$module")
  install -Dm0644 "$module_dir/LICENSE" "$package_root/bundle/share/licenses/go/x-$module/LICENSE"
done
openssl_license=
for candidate in /usr/share/doc/libssl3/copyright /usr/share/licenses/openssl/LICENSE.txt /usr/share/licenses/openssl/LICENSE; do
  if [[ -f "$candidate" ]]; then openssl_license=$candidate; break; fi
done
[[ -n "$openssl_license" ]] || { echo 'OpenSSL license inventory not found' >&2; exit 1; }
install -m 0644 "$openssl_license" "$package_root/bundle/share/licenses/openssl/$(basename "$openssl_license")"
"$manager" materialize-links --root "$package_root"
while IFS= read -r -d '' library; do
  relative_lib=$(realpath --relative-to="$(dirname "$library")" "$package_root/bundle/lib")
  patchelf --set-rpath "\$ORIGIN/$relative_lib" "$library"
done < <(find "$package_root/bundle/plugins" "$package_root/bundle/qml" -type f -name '*.so*' -print0)
env -u QT_PLUGIN_PATH -u QML2_IMPORT_PATH -u QML_IMPORT_PATH -u QT_QPA_PLATFORM_PLUGIN_PATH \
  "$manager" create-package --root "$package_root" --output "$output_dir/$asset" \
    --version "$version" --platform linux --arch x86_64 --qt-version "$qt_version" \
    --baseline ubuntu-22.04-glibc-2.35
"$manager" verify --archive "$output_dir/$asset" --version "$version" --platform linux --arch x86_64 --asset "$asset"
env -u QT_PLUGIN_PATH -u QML2_IMPORT_PATH -u QML_IMPORT_PATH -u QT_QPA_PLATFORM_PLUGIN_PATH \
  LD_LIBRARY_PATH="$package_root/bundle/lib" ldd "$package_root/bundle/bin/headroom" | tee "$output_dir/linux-runtime-dependencies.txt"
if grep -F 'not found' "$output_dir/linux-runtime-dependencies.txt"; then exit 1; fi
