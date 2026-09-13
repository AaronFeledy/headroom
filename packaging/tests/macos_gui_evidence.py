"""Shared macOS GUI screenshot checks for packaged Headroom smoke.

Capture mode uses an isolated config and never polls providers. This helper
must not grow reset-credit, ConsumeResetCredit, or CodexReset coverage.
"""

from __future__ import annotations

import hashlib
import json
from pathlib import Path
import struct
import zlib


PNG_SIGNATURE = b"\x89PNG\r\n\x1a\n"


def installer_manifest(archive: Path, version: str, architecture: str) -> dict:
    digest = hashlib.sha256()
    with archive.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return {
        "schema": 1,
        "product": "Headroom",
        "version": version,
        "packages": [{
            "platform": "macos",
            "architecture": architecture,
            "asset_name": archive.name,
            "size": archive.stat().st_size,
            "sha256": digest.hexdigest(),
        }],
    }


def write_installer_manifest(archive: Path, destination: Path, version: str, architecture: str) -> Path:
    destination.write_text(json.dumps(installer_manifest(archive, version, architecture), indent=2) + "\n")
    return destination


def png_header(path: Path) -> dict:
    data = path.read_bytes()
    if len(data) < 33 or data[:8] != PNG_SIGNATURE:
        raise AssertionError(f"{path} is not a PNG screenshot")
    length = int.from_bytes(data[8:12], "big")
    if data[12:16] != b"IHDR" or length != 13:
        raise AssertionError(f"{path} is missing a PNG IHDR")
    width, height, bit_depth, color_type = struct.unpack(">IIBB", data[16:26])
    if width < 1 or height < 1:
        raise AssertionError(f"{path} has an empty PNG frame")
    return {
        "path": str(path),
        "bytes": len(data),
        "width": width,
        "height": height,
        "bit_depth": bit_depth,
        "color_type": color_type,
        "sha256": hashlib.sha256(data).hexdigest(),
    }


def _png_rows(data: bytes) -> tuple[int, int, int, list[bytes]]:
    position = 8
    width = height = bit_depth = color_type = None
    idat = bytearray()
    while position + 12 <= len(data):
        length = int.from_bytes(data[position:position + 4], "big")
        chunk = data[position + 4:position + 8]
        payload = data[position + 8:position + 8 + length]
        if chunk == b"IHDR":
            width, height, bit_depth, color_type = struct.unpack(">IIBB", payload[:10])
        elif chunk == b"IDAT":
            idat.extend(payload)
        elif chunk == b"IEND":
            break
        position += 12 + length
    if None in (width, height, bit_depth, color_type):
        raise AssertionError("PNG IHDR is incomplete")
    if bit_depth != 8 or color_type not in (2, 6):
        raise AssertionError("screenshot PNG must be 8-bit RGB or RGBA")
    channels = 4 if color_type == 6 else 3
    raw = zlib.decompress(bytes(idat))
    stride = width * channels
    expected = height * (1 + stride)
    if len(raw) != expected:
        raise AssertionError("PNG IDAT size does not match the IHDR frame")
    rows = []
    prior = bytearray(stride)
    for index in range(height):
        start = index * (1 + stride)
        filter_type = raw[start]
        scan = bytearray(raw[start + 1:start + 1 + stride])
        if filter_type == 1:
            for column in range(stride):
                left = scan[column - channels] if column >= channels else 0
                scan[column] = (scan[column] + left) & 255
        elif filter_type == 2:
            for column in range(stride):
                scan[column] = (scan[column] + prior[column]) & 255
        elif filter_type == 3:
            for column in range(stride):
                left = scan[column - channels] if column >= channels else 0
                scan[column] = (scan[column] + ((left + prior[column]) // 2)) & 255
        elif filter_type == 4:
            for column in range(stride):
                left = scan[column - channels] if column >= channels else 0
                up = prior[column]
                up_left = prior[column - channels] if column >= channels else 0
                predictor = left + up - up_left
                distances = (abs(predictor - left), abs(predictor - up), abs(predictor - up_left))
                paeth = (left, up, up_left)[distances.index(min(distances))]
                scan[column] = (scan[column] + paeth) & 255
        elif filter_type != 0:
            raise AssertionError(f"unsupported PNG filter {filter_type}")
        rows.append(bytes(scan))
        prior = scan
    return width, height, channels, rows


def assert_rendered_png(path: Path, minimum_width=400, minimum_height=400, minimum_bytes=12000):
    header = png_header(path)
    if header["bytes"] < minimum_bytes:
        raise AssertionError(f"{path} is too small to be a rendered Headroom window ({header['bytes']} bytes)")
    if header["width"] < minimum_width or header["height"] < minimum_height:
        raise AssertionError(
            f"{path} is {header['width']}x{header['height']}, below the {minimum_width}x{minimum_height} GUI floor")
    width, height, channels, rows = _png_rows(path.read_bytes())
    opaque = 0
    colors = set()
    step = max(1, width // 80)
    for row in rows[::max(1, height // 80)]:
        for column in range(0, width, step):
            offset = column * channels
            red, green, blue = row[offset:offset + 3]
            alpha = row[offset + 3] if channels == 4 else 255
            if alpha < 128:
                continue
            opaque += 1
            colors.add((red // 16, green // 16, blue // 16))
    if opaque < 40:
        raise AssertionError(f"{path} has too few opaque pixels to prove a GUI render")
    if len(colors) < 3:
        raise AssertionError(f"{path} looks blank or single-color ({len(colors)} quantized colors)")
    header["opaque_samples"] = opaque
    header["quantized_colors"] = len(colors)
    return header


def write_evidence(path: Path, payload: dict):
    path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n")
