"""Download a pinned official BGE-M3 ONNX model; never execute repository code."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import urllib.request

REVISION = "5617a9f61b028005a4858fdac845db406aefb181"
FILES = {
    "model.onnx": (724923, "sha256", "f84251230831afb359ab26d9fd37d5936d4d9bb5d1d5410e66442f630f24435b"),
    "model.onnx_data": (2266820608, "sha256", "1eebfb28493f67bba03ce0ef64bfdc7fc5a3bd9d7493f818bb1d78cd798416b4"),
    "Constant_7_attr__value": (65552, "git", "3cdc05e5f550d8bd136f28efcea0ad5e6b4169c8"),
    "config.json": (698, "git", "aadef53b97586be7cafa2c06837a0bd53f1f1ded"),
    "tokenizer.json": (17082821, "sha256", "6710678b12670bc442b99edc952c4d996ae309a7020c1fa0096dd245c2faf790"),
}


def digests(path: Path, size: int) -> tuple[str, str]:
    sha = hashlib.sha256()
    git = hashlib.sha1(f"blob {size}\0".encode())
    with path.open("rb") as stream:
        while chunk := stream.read(1024 * 1024):
            sha.update(chunk)
            git.update(chunk)
    return sha.hexdigest(), git.hexdigest()


def verified(path: Path, entry: tuple[int, str, str]) -> bool:
    size, kind, expected = entry
    if not path.is_file() or path.stat().st_size != size:
        return False
    sha, git = digests(path, size)
    return (sha if kind == "sha256" else git) == expected


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--destination", type=Path, required=True)
    parser.add_argument("--manifest", type=Path, required=True)
    args = parser.parse_args()
    args.destination.mkdir(parents=True, exist_ok=True)
    manifest = {"model": "BAAI/bge-m3", "revision": REVISION, "dimension": 1024,
                "format": "official-onnx-float32", "files": {}}
    for name, entry in FILES.items():
        path = args.destination / name
        if not verified(path, entry):
            partial = path.with_name(name + ".partial")
            offset = partial.stat().st_size if partial.exists() else 0
            request = urllib.request.Request(
                f"https://huggingface.co/BAAI/bge-m3/resolve/{REVISION}/onnx/{name}",
                headers={"Range": f"bytes={offset}-"} if offset else {},
            )
            print(f"Downloading official {name} ({entry[0]} bytes)", flush=True)
            with urllib.request.urlopen(request, timeout=120) as response:
                mode = "ab" if offset and response.status == 206 else "wb"
                with partial.open(mode) as output:
                    while chunk := response.read(1024 * 1024):
                        output.write(chunk)
                    output.flush()
                    os.fsync(output.fileno())
            if not verified(partial, entry):
                raise RuntimeError(f"Official model checksum mismatch: {name}")
            partial.replace(path)
        sha, _ = digests(path, entry[0])
        manifest["files"][name] = {"size": entry[0], "sha256": sha}
        print(f"Verified {name}", flush=True)
    args.manifest.parent.mkdir(parents=True, exist_ok=True)
    args.manifest.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
