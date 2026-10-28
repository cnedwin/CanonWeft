"""Build deterministic Linux initramfs without extracting Unix links on Windows."""

import gzip
import hashlib
import io
import json
from pathlib import Path, PurePosixPath
import stat
import sys
import tarfile


def archive_entries(path):
    # APK v2 contains concatenated gzip/tar streams. Ignore tar EOF records.
    data = gzip.decompress(path.read_bytes())
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:", ignore_zeros=True) as archive:
        members = list(archive)
        by_name = {member.name: member for member in members}
        for member in members:
            name = member.name.rstrip("/")
            if not name or name.startswith("."):
                continue
            if PurePosixPath(name).is_absolute() or ".." in PurePosixPath(name).parts:
                raise ValueError("unsafe package path")
            if member.isdir():
                yield name, stat.S_IFDIR | 0o755, b""
            elif member.issym():
                yield name, stat.S_IFLNK | 0o777, member.linkname.encode()
            elif member.isfile() or member.islnk():
                if member.islnk():
                    member = by_name[member.linkname]
                yield name, stat.S_IFREG | (member.mode & 0o755), archive.extractfile(member).read()


def cpio(entries):
    result = bytearray()
    for inode, (name, (mode, data)) in enumerate(sorted(entries.items()) + [("TRAILER!!!", (0, b""))], 1):
        encoded = name.encode() + b"\0"
        fields = [inode, mode, 0, 0, 1, 0, len(data), 0, 0, 0, 0, len(encoded), 0]
        result.extend(b"070701" + b"".join(f"{value:08x}".encode() for value in fields))
        result.extend(encoded)
        result.extend(b"\0" * (-len(result) % 4))
        result.extend(data)
        result.extend(b"\0" * (-len(result) % 4))
    return gzip.compress(bytes(result), mtime=0)


def build(cache, destination):
    source = Path(__file__).parent
    lock = json.loads((source / "sources.lock.json").read_text())
    entries = {}
    for item in lock["sources"]:
        path = cache / item["name"]
        if hashlib.sha256(path.read_bytes()).hexdigest() != item["sha256"]:
            raise ValueError("source hash mismatch: " + item["name"])
        if path.suffix == ".apk":
            for name, mode, data in archive_entries(path):
                entries[name] = (mode, data)
    for name in ["dev", "proc", "sys", "work", "input", "tmp", "etc", "bin"]:
        entries[name] = (stat.S_IFDIR | 0o755, b"")
    entries["bin/sh"] = (stat.S_IFLNK | 0o777, b"busybox")
    entries["init"] = (stat.S_IFREG | 0o755, (source / "init").read_bytes().replace(b"\r\n", b"\n"))
    entries["guest.py"] = (stat.S_IFREG | 0o644, (source / "guest.py").read_bytes().replace(b"\r\n", b"\n"))
    entries["etc/passwd"] = (stat.S_IFREG | 0o644, b"root:x:0:0:root:/:/bin/sh\nworker:x:1000:1000:worker:/work:/bin/sh\n")
    entries["etc/group"] = (stat.S_IFREG | 0o644, b"root:x:0:\nworker:x:1000:\n")
    destination.mkdir(parents=True, exist_ok=True)
    (destination / "initramfs.gz").write_bytes(cpio(entries))
    (destination / "vmlinuz-virt").write_bytes((cache / "vmlinuz-virt").read_bytes())
    files = {}
    for path in sorted(destination.rglob("*")):
        if path.is_file() and path.name not in ("manifest.json", "image.sha256"):
            files[path.relative_to(destination).as_posix()] = hashlib.sha256(path.read_bytes()).hexdigest()
    manifest = json.dumps({"format": 1, "files": files}, sort_keys=True, separators=(",", ":")).encode()
    (destination / "manifest.json").write_bytes(manifest)
    digest = hashlib.sha256(manifest).hexdigest()
    (destination / "image.sha256").write_text(digest + "\n")
    print("Built isolated runtime " + digest)


if __name__ == "__main__":
    build(Path(sys.argv[1]), Path(sys.argv[2]))
