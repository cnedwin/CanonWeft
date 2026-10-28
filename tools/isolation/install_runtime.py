"""Verified user-space installation. Never executes an installer or elevates."""

import concurrent.futures
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import urllib.request

from build_image import build


def acquire(cache, item):
    target = cache / item["name"]
    if not target.exists():
        partial = target.with_suffix(target.suffix + ".download")
        try:
            with urllib.request.urlopen(item["url"], timeout=120) as source, partial.open("wb") as output:
                total = 0
                while chunk := source.read(1024 * 1024):
                    total += len(chunk)
                    if total > 256 * 1024 * 1024:
                        raise ValueError("download exceeds package size policy")
                    output.write(chunk)
            if hashlib.sha256(partial.read_bytes()).hexdigest() != item["sha256"]:
                raise ValueError("download hash mismatch: " + item["name"])
            partial.replace(target)
        finally:
            partial.unlink(missing_ok=True)
    if hashlib.sha256(target.read_bytes()).hexdigest() != item["sha256"]:
        raise ValueError("cached source hash mismatch: " + item["name"])


def install(cache):
    cache.mkdir(parents=True, exist_ok=True)
    lock = json.loads((Path(__file__).parent / "sources.lock.json").read_text())
    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as executor:
        list(executor.map(lambda item: acquire(cache, item), lock["sources"]))
    if hashlib.sha512((cache / "qemu-setup.exe").read_bytes()).hexdigest() != lock["qemu_publisher_sha512"]:
        raise ValueError("QEMU publisher SHA-512 mismatch")
    extractor = cache / "7zip-26.04"
    subprocess.run([str(cache / "7zr-versioned.exe"), "x", str(cache / "7z2604-x64.exe"), "-o" + str(extractor), "-y"], check=True)
    runtime = cache / "runtime"
    # Existing installations are never silently deleted or relaxed; unexpected
    # files change the manifest and fail the final pinned image comparison.
    subprocess.run([
        str(extractor / "7z.exe"), "x", str(cache / "qemu-setup.exe"),
        "-o" + str(runtime / "qemu"), "-y", "qemu-system-x86_64.exe", "*.dll",
        "share/bios-256k.bin", "share/kvmvapic.bin", "share/linuxboot_dma.bin",
    ], check=True)
    build(cache, runtime)
    if (runtime / "image.sha256").read_text().strip() != lock["image_sha256"]:
        raise ValueError("built runtime differs from the reviewed image lock")
    print("Verified Linux runtime ready: " + str(runtime))


if __name__ == "__main__":
    install(Path(sys.argv[1]).resolve())
