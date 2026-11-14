import importlib.util
import io
import pathlib
import re
import subprocess
import sys
import tempfile
import unittest


MODULE_PATH = pathlib.Path(__file__).parents[1] / "claude_code_contract.py"
SPEC = importlib.util.spec_from_file_location("claude_code_contract", MODULE_PATH)
assert SPEC is not None
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader
SPEC.loader.exec_module(MODULE)

# Independent synthetic excerpts of the upstream contract, separated like binary strings.
EXCERPTS = {
    "service credentials suffix": b'let suffix="-credentials";',
    "service OAuth suffix template": b'`Claude Code${config.OAUTH_FILE_SUFFIX}${suffix}${hash}`',
    "service directory hash": b'.digest("hex").substring(0,8)',
    "service directory NFC": b'.normalize("NFC")',
    "account USER fallback": b'process.env.USER||os.userInfo().username',
    "account default": b'"claude-code-user"',
    "account safe characters": b'/^[a-zA-Z0-9._-]+$/',
    "keychain read argv": b'["find-generic-password","-a",account,"-w","-s",service]',
    "plaintext credentials file": b'".credentials.json"',
    "keychain fallback composition": b'`${primary.name}-with-${fallback.name}-fallback`',
}
BLOB = b"\x00".join(EXCERPTS.values())


class ClaudeCodeContractTests(unittest.TestCase):
    def test_all_contract_markers_pass(self):
        self.assertEqual(MODULE.missing_markers(io.BytesIO(BLOB)), ())

    def test_each_missing_marker_is_named(self):
        for name, excerpt in EXCERPTS.items():
            with self.subTest(marker=name):
                self.assertEqual(
                    MODULE.missing_markers(io.BytesIO(BLOB.replace(excerpt, b""))),
                    (name,),
                )

    def test_markers_cross_chunk_boundaries(self):
        for excerpt in EXCERPTS.values():
            with self.subTest(excerpt=excerpt):
                blob = b"\x00" * (MODULE.CHUNK_SIZE - len(excerpt) // 2) + excerpt + b"\x00" + BLOB.replace(excerpt, b"")
                self.assertEqual(MODULE.missing_markers(io.BytesIO(blob)), ())

    def test_minified_identifier_renames_preserve_markers(self):
        renamed = re.sub(
            rb'\b(config|suffix|hash|os|account|service|primary)\b',
            b"x", BLOB,
        ).replace(b"${fallback.name}", b"${y.name}")
        self.assertEqual(MODULE.missing_markers(io.BytesIO(renamed)), ())

    def test_offline_cli_reports_success_and_missing_markers(self):
        with tempfile.TemporaryDirectory() as directory:
            binary = pathlib.Path(directory) / "claude"
            for blob, status in ((BLOB, 0), (BLOB.replace(EXCERPTS["keychain read argv"], b""), 1)):
                with self.subTest(status=status):
                    binary.write_bytes(blob)
                    result = subprocess.run(
                        [sys.executable, str(MODULE_PATH), "--binary", str(binary)],
                        capture_output=True, text=True, check=False,
                    )
                    self.assertEqual(result.returncode, status, result.stderr)
                    if status:
                        self.assertIn("keychain read argv", result.stderr)
                    else:
                        self.assertIn("all 10 markers found", result.stdout)


if __name__ == "__main__":
    unittest.main()
