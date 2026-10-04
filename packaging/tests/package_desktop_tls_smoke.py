#!/usr/bin/env python3
"""Exercise the installed Qt desktop's owned Go TLS session, without a Qt SDK.

Screenshots deliberately disable networking; this separate hidden diagnostic
waits for a pinned HTTPS usage response from the adjacent owned usage-server.
"""
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile


def main() -> None:
    desktop = Path(sys.argv[1]).resolve(strict=True)
    expected_backend = {"linux": "openssl", "win32": "schannel", "darwin": "securetransport"}[sys.platform]
    with socket.socket() as probe:
        probe.settimeout(1)
        if probe.connect_ex(("127.0.0.1", 7823)) == 0:
            raise SystemExit("Packaged TLS smoke requires unused localhost port 7823; refusing to attach to an existing server.")
    environment = {key: value for key, value in os.environ.items()
                   if not key.startswith(("QT_", "QML", "HEADROOM_", "DYLD_", "USAGE_"))
                   and key != "LD_LIBRARY_PATH"}
    environment.update({"QT_QPA_PLATFORM": "offscreen", "QT_QUICK_BACKEND": "software"})
    with tempfile.TemporaryDirectory(prefix="headroom-packaged-tls-") as temporary:
        root = Path(temporary)
        config = root / "server.yaml"
        config.write_text("providers:\n" + "".join(f"  {name}: {{enabled: false}}\n"
                          for name in ("claude", "codex", "cursor", "grok")), encoding="utf-8")
        environment["USAGE_CONFIG"] = str(config)
        environment["XDG_CONFIG_HOME"] = str(root)
        if sys.platform == "win32":
            environment["APPDATA"] = str(root)
        for provider in ("CLAUDE", "CODEX", "CURSOR", "GROK"):
            environment[f"USAGE_PROVIDER_{provider}_ENABLED"] = "false"
        result = root / "tls.json"
        subprocess.run([str(desktop), "--config", str(root / "settings.json"),
                        "--headroom-tls-smoke-file", str(result)], env=environment, check=True, timeout=40)
        value = json.loads(result.read_text(encoding="utf-8"))
        assert value["ok"] is True and value["backend"] == expected_backend, value
        assert value["qt_version"] == "6.12.0", value
        print(f"Packaged Qt {value['qt_version']} {value['backend']}: owned desktop-session TLS and authenticated usage PASS")


if __name__ == "__main__":
    main()
