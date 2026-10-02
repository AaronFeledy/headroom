#!/usr/bin/env python3
"""Fetch and package a verified, versioned Qt attribution inventory."""

import argparse
import hashlib
import json
import os
import pathlib
import posixpath
import re
import shutil
import stat
import tempfile
import urllib.parse
import urllib.request
import zipfile

MAX_SOURCE_BYTES = 128 * 1024 * 1024
MAX_MEMBER_BYTES = 16 * 1024 * 1024
MAX_SBOM_BYTES = 64 * 1024 * 1024
TOKEN = re.compile(r"[A-Za-z0-9][A-Za-z0-9.+-]*")


def sha256(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def load_manifest(path: pathlib.Path) -> dict:
    value = json.loads(path.read_text(encoding="utf-8"))
    if value.get("schema") != 1 or not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", str(value.get("qt_version", ""))):
        raise ValueError("Qt source manifest is invalid")
    sources = value.get("sources")
    if not isinstance(sources, list) or len(sources) != 5:
        raise ValueError("Qt source manifest must contain five modules")
    seen = set()
    for source in sources:
        required = {"module", "archive", "url", "size", "sha256"}
        if set(source) != required or source["module"] in seen:
            raise ValueError("Qt source entry is invalid or duplicated")
        seen.add(source["module"])
        parsed = urllib.parse.urlparse(source["url"])
        if parsed.scheme != "https" or parsed.hostname != "download.qt.io" or parsed.username or parsed.password or parsed.port not in (None, 443):
            raise ValueError("Qt source URL is not an official HTTPS URL")
        if pathlib.PurePosixPath(parsed.path).name != source["archive"] or not 0 < source["size"] <= MAX_SOURCE_BYTES:
            raise ValueError("Qt source archive identity is invalid")
        if not re.fullmatch(r"[0-9a-f]{64}", source["sha256"]):
            raise ValueError("Qt source digest is invalid")
    return value


def verify_archive(path: pathlib.Path, source: dict) -> None:
    if not path.is_file() or path.stat().st_size != source["size"] or sha256(path) != source["sha256"]:
        raise ValueError(f"Qt source archive failed verification: {source['archive']}")


def fetch_sources(manifest: dict, cache: pathlib.Path) -> None:
    cache.mkdir(parents=True, exist_ok=True)
    for source in manifest["sources"]:
        destination = cache / source["archive"]
        if destination.exists():
            verify_archive(destination, source)
            continue
        fd, temporary_name = tempfile.mkstemp(prefix=".qt-source-", dir=cache)
        os.close(fd)
        temporary = pathlib.Path(temporary_name)
        try:
            request = urllib.request.Request(source["url"], headers={"User-Agent": "Headroom-attribution-builder"})
            with urllib.request.urlopen(request, timeout=120) as response, temporary.open("wb") as output:
                written = 0
                while True:
                    block = response.read(min(1024 * 1024, source["size"] - written + 1))
                    if not block:
                        break
                    written += len(block)
                    if written > source["size"]:
                        raise ValueError("Qt source archive exceeded its recorded size")
                    output.write(block)
            verify_archive(temporary, source)
            temporary.replace(destination)
        finally:
            temporary.unlink(missing_ok=True)


def safe_members(archive: zipfile.ZipFile, expected_root: str) -> dict[str, zipfile.ZipInfo]:
    result = {}
    for info in archive.infolist():
        name = info.filename
        normalized = posixpath.normpath(name)
        mode = info.external_attr >> 16
        if (name.startswith("/") or "\\" in name or normalized != name.rstrip("/")
                or (normalized != expected_root and not normalized.startswith(expected_root + "/")) or stat.S_ISLNK(mode)):
            raise ValueError(f"unsafe Qt source member: {name}")
        if not info.is_dir() and info.file_size > MAX_MEMBER_BYTES:
            raise ValueError(f"oversized Qt attribution member: {name}")
        if normalized in result:
            raise ValueError(f"duplicate Qt source member: {name}")
        result[normalized] = info
    return result


def records(value, path: str) -> list[dict]:
    items = value if isinstance(value, list) else [value]
    if not items or any(not isinstance(item, dict) for item in items):
        raise ValueError(f"invalid Qt attribution JSON: {path}")
    for item in items:
        if any(not isinstance(item.get(key), str) or not item[key] for key in ("Id", "Name", "LicenseId")):
            raise ValueError(f"incomplete Qt attribution record: {path}")
    return items


def reference_names(record: dict) -> list[str]:
    result = []
    for key in ("LicenseFile", "CopyrightFile"):
        value = record.get(key)
        if value is not None:
            if not isinstance(value, str) or not value:
                raise ValueError(f"invalid {key}")
            result.append(value)
    values = record.get("LicenseFiles", [])
    if isinstance(values, str):
        values = [values]
    if not isinstance(values, list) or any(not isinstance(value, str) or not value for value in values):
        raise ValueError("invalid LicenseFiles")
    return result + values


def copy_member(archive: zipfile.ZipFile, members: dict, name: str, root: str, destination: pathlib.Path) -> None:
    normalized = posixpath.normpath(name)
    if normalized not in members or members[normalized].is_dir() or not normalized.startswith(root + "/"):
        raise ValueError(f"referenced Qt notice is missing: {name}")
    relative = pathlib.PurePosixPath(normalized).relative_to(root)
    target = destination.joinpath(*relative.parts)
    target.parent.mkdir(parents=True, exist_ok=True)
    with archive.open(members[normalized]) as source, target.open("wb") as output:
        shutil.copyfileobj(source, output)


def payload_matches(module: str, payload: pathlib.Path) -> list[str]:
    needles = {
        "qtbase": ("qt6core", "qt6gui", "qt6network", "qt6widgets", "platforms/"),
        "qtdeclarative": ("qt6qml", "qt6quick", "/qml/"),
        "qtshadertools": ("qt6shadertools", "/qsb", "qsb.exe"),
        "qtsvg": ("qt6svg", "qsvg"),
        "qtwayland": ("qt6wayland", "qwayland", "wayland"),
    }[module]
    matches = []
    for path in payload.rglob("*"):
        if path.is_file() and not path.is_symlink():
            relative = path.relative_to(payload).as_posix()
            if relative == "share/licenses" or relative.startswith("share/licenses/"):
                continue
            lowered = "/" + relative.lower()
            if any(needle in lowered for needle in needles):
                matches.append(relative)
    return sorted(matches)


def copy_kit_sbom(qt_root: pathlib.Path, output: pathlib.Path) -> list[dict]:
    source = qt_root / "sbom"
    if not source.is_dir() or source.is_symlink():
        return []
    copied, total = [], 0
    for path in sorted(source.rglob("*")):
        if not path.is_file() or path.is_symlink():
            continue
        total += path.stat().st_size
        if total > MAX_SBOM_BYTES:
            raise ValueError("installed Qt SBOM exceeds the package limit")
        relative = path.relative_to(source)
        target = output / "kit-sbom" / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(path, target)
        copied.append({"path": relative.as_posix(), "sha256": sha256(target), "size": target.stat().st_size})
    return copied


def collect(manifest: dict, cache: pathlib.Path, qt_root: pathlib.Path, payload: pathlib.Path,
            output: pathlib.Path, selected: list[str]) -> None:
    if not cache.is_dir() or not qt_root.is_dir() or not payload.is_dir():
        raise ValueError("Qt source cache, Qt kit, and deployed payload must be directories")
    if output.exists() and any(output.iterdir()):
        raise ValueError("Qt attribution output must be empty")
    output.mkdir(parents=True, exist_ok=True)
    known = {source["module"]: source for source in manifest["sources"]}
    if len(selected) != len(set(selected)) or any(module not in known for module in selected):
        raise ValueError("unknown or duplicate Qt source module")
    inventory = {
        "schema": 1,
        "qt_version": manifest["qt_version"],
        "inventory_kind": "conservative source-module attribution superset; not an exact binary SBOM",
        "modules": [],
        "kit_sbom_present": False,
        "kit_sbom_files": [],
    }
    for module in selected:
        source = known[module]
        archive_path = cache / source["archive"]
        verify_archive(archive_path, source)
        expected_root = source["archive"].removesuffix(".zip")
        module_destination = output / "sources" / module
        summaries, copied = [], set()
        with zipfile.ZipFile(archive_path) as archive:
            members = safe_members(archive, expected_root)
            attribution_paths = sorted(name for name in members if name.endswith("/qt_attribution.json"))
            if not attribution_paths and not (module == "qtsvg" and manifest["qt_version"] == "6.12.0"):
                raise ValueError(f"Qt module has no attribution records: {module}")
            if not attribution_paths:
                for metadata in ("REUSE.toml", "licenseRule.json"):
                    copy_member(archive, members, expected_root + "/" + metadata, expected_root, module_destination)
            license_prefix = expected_root + "/LICENSES/"
            for name, info in members.items():
                if name.startswith(license_prefix) and not info.is_dir():
                    copy_member(archive, members, name, expected_root, module_destination)
                    copied.add(name)
            if not copied:
                raise ValueError(f"Qt module has no license texts: {module}")
            for attribution_path in attribution_paths:
                raw = archive.read(members[attribution_path])
                parsed = json.loads(raw.decode("utf-8"), strict=False)
                attribution_records = records(parsed, attribution_path)
                copy_member(archive, members, attribution_path, expected_root, module_destination)
                copied.add(attribution_path)
                base = posixpath.dirname(attribution_path)
                for record in attribution_records:
                    references = reference_names(record)
                    for reference in references:
                        name = posixpath.normpath(posixpath.join(base, reference))
                        copy_member(archive, members, name, expected_root, module_destination)
                        copied.add(name)
                    if not any(key in record for key in ("LicenseFile", "LicenseFiles")):
                        identifiers = [item for item in TOKEN.findall(record["LicenseId"]) if item not in {"AND", "OR", "WITH"}]
                        for identifier in identifiers:
                            if expected_root + "/LICENSES/" + identifier + ".txt" not in members:
                                raise ValueError(f"license text missing for {record['Id']}: {identifier}")
                    parts = record.get("QtParts") or []
                    if isinstance(parts, str):
                        parts = [parts]
                    if not isinstance(parts, list) or any(not isinstance(part, str) for part in parts):
                        raise ValueError(f"invalid QtParts for {record['Id']}")
                    provenance = any(part.lower() in {"examples", "tests", "tools"} for part in parts) or \
                        any(word in (record.get("QtUsage", "") + " " + attribution_path).lower() for word in ("example", "build system", "/tests/"))
                    summaries.append({
                        "id": record["Id"], "name": record["Name"], "license_id": record["LicenseId"],
                        "attribution_path": pathlib.PurePosixPath(attribution_path).relative_to(expected_root).as_posix(),
                        "classification": "source_or_build_provenance" if provenance else "module_source_attribution",
                        "qt_parts": parts, "qt_usage": record.get("QtUsage", ""),
                    })
        matches = payload_matches(module, payload)
        inventory["modules"].append({
            "module": module, "source_url": source["url"], "source_size": source["size"],
            "source_sha256": source["sha256"], "payload_role": "deployed_runtime" if matches else "build_provenance",
            "payload_matches": matches, "attribution_records": sorted(summaries, key=lambda item: (item["attribution_path"], item["id"])),
        })
    inventory["kit_sbom_files"] = copy_kit_sbom(qt_root, output)
    inventory["kit_sbom_present"] = bool(inventory["kit_sbom_files"])
    (output / "index.json").write_text(json.dumps(inventory, indent=2, sort_keys=True) + "\n", encoding="utf-8", newline="\n")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--manifest", default=str(pathlib.Path(__file__).with_name("qt-sources-6.12.0.json")))
    subparsers = parser.add_subparsers(dest="command", required=True)
    fetch = subparsers.add_parser("fetch")
    fetch.add_argument("--source-cache", required=True)
    package = subparsers.add_parser("package")
    package.add_argument("--source-cache", required=True)
    package.add_argument("--qt-root", required=True)
    package.add_argument("--payload-root", required=True)
    package.add_argument("--output", required=True)
    package.add_argument("--modules", nargs="+", required=True)
    args = parser.parse_args()
    manifest = load_manifest(pathlib.Path(args.manifest))
    cache = pathlib.Path(args.source_cache)
    if args.command == "fetch":
        fetch_sources(manifest, cache)
    else:
        collect(manifest, cache, pathlib.Path(args.qt_root), pathlib.Path(args.payload_root), pathlib.Path(args.output), args.modules)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
