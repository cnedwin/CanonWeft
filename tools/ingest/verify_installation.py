"""Validate versions, installed wheel RECORD hashes, and bundled model hashes offline."""
import base64
import hashlib
import importlib.metadata
import json
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent))
from worker import MANIFEST, verify_installation


def verify():
    manifest = json.loads(MANIFEST.read_text(encoding="utf-8"))
    if sys.version_info[:2] != (3, 12) or sys.platform != "win32":
        raise RuntimeError("Locked platform is Windows CPython 3.12")
    count = 0
    for name, version in manifest["versions"].items():
        package = importlib.metadata.distribution(name)
        if package.version != version:
            raise RuntimeError(f"Version mismatch: {name}")
        for entry in package.files or []:
            if entry.hash is None:
                continue  # Generated pyc and RECORD have no wheel hash.
            if entry.hash.mode != "sha256":
                raise RuntimeError("Unexpected installed artifact hash algorithm")
            digest = hashlib.sha256(package.locate_file(entry).read_bytes()).digest()
            if base64.urlsafe_b64encode(digest).rstrip(b"=").decode() != entry.hash.value:
                raise RuntimeError(f"Installed artifact checksum mismatch: {name}")
            count += 1
    verify_installation()
    return count


if __name__ == "__main__":
    print(f"Verified offline ingest: {verify()} installed artifacts and 3 bundled OCR models")
