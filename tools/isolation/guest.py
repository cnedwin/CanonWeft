"""PID 1 supervisor. Input is an approved copy in the one-use initramfs."""

import base64
import ctypes
import json
import os
from pathlib import Path
import unicodedata
import resource
import selectors
import signal
import subprocess
import sys
import time

MAX_REQUEST = 16 * 1024 * 1024


def restrict_child():
    os.setsid()
    resource.setrlimit(resource.RLIMIT_AS, (96 * 1024 * 1024,) * 2)
    resource.setrlimit(resource.RLIMIT_CPU, (31, 31))
    resource.setrlimit(resource.RLIMIT_NPROC, (32, 32))
    resource.setrlimit(resource.RLIMIT_NOFILE, (64, 64))
    resource.setrlimit(resource.RLIMIT_FSIZE, (16 * 1024 * 1024,) * 2)
    resource.setrlimit(resource.RLIMIT_CORE, (0, 0))
    # PR_SET_NO_NEW_PRIVS survives exec and prevents setuid/capability gain.
    if ctypes.CDLL(None).prctl(38, 1, 0, 0, 0) != 0:
        raise OSError("cannot set no_new_privs")
    os.setgroups([])
    os.setgid(1000)
    os.setuid(1000)


def decode_bytes(value, limit):
    if not isinstance(value, str) or len(value) > ((limit + 2) // 3) * 4:
        raise ValueError("encoded input too large")
    data = base64.b64decode(value, validate=True)
    if len(data) > limit:
        raise ValueError("input too large")
    return data


def valid_path(value):
    if not isinstance(value, str) or not value or len(value.encode("utf-8")) > 1024:
        return False
    if any(c in value for c in '\\:<>"|?*') or any(unicodedata.category(c) == "Cc" for c in value):
        return False
    parts = value.split("/")
    if len(parts) > 32:
        return False
    for part in parts:
        if not part or part in (".", "..") or len(part.encode("utf-8")) > 255 or part.endswith((".", " ")):
            return False
        stem = part.split(".", 1)[0].upper()
        if stem in ("CON", "PRN", "AUX", "NUL", "CLOCK$", "CONIN$", "CONOUT$"):
            return False
        if len(stem) == 4 and stem[:3] in ("COM", "LPT") and stem[3] in "123456789¹²³":
            return False
    return True


def prepare_inputs(request):
    files = request.get("files", [])
    entrypoint = request.get("entrypoint", "")
    script = request.get("script", "")
    if not files and not entrypoint:
        encoded = script.encode("utf-8")
        if not encoded or len(encoded) > 256 * 1024:
            raise ValueError("invalid script")
        with open("/input/script", "xb") as target:
            target.write(encoded)
        os.chmod("/input/script", 0o444)
        return "/input/script", "/work"
    if script or not isinstance(files, list) or not 1 <= len(files) <= 128 or not valid_path(entrypoint):
        raise ValueError("invalid package mode")
    approved = []
    names = {}
    total = 0
    found = False
    for item in files:
        if not isinstance(item, dict) or set(item) != {"path", "data"} or not valid_path(item["path"]):
            raise ValueError("invalid package file")
        path = item["path"]
        parts = path.split("/")
        for index in range(len(parts)):
            name = "/".join(parts[:index + 1])
            key = name.casefold()
            is_file = index == len(parts) - 1
            previous = names.get(key)
            if previous and (previous[0] != name or previous[1] or is_file):
                raise ValueError("conflicting package path")
            names[key] = (name, is_file)
        data = decode_bytes(item["data"], 2 * 1024 * 1024)
        total += len(data)
        if total > 8 * 1024 * 1024:
            raise ValueError("package too large")
        if path == entrypoint:
            found = True
            if not data or len(data) > 256 * 1024:
                raise ValueError("invalid entrypoint size")
            data.decode("utf-8", errors="strict")
        approved.append((path, data))
    if not found:
        raise ValueError("missing entrypoint")
    root = Path("/input/package")
    root.mkdir(mode=0o755)
    for path, data in approved:
        destination = root / path
        destination.parent.mkdir(parents=True, exist_ok=True)
        with destination.open("xb") as target:
            target.write(data)
        destination.chmod(0o444)
    # Root-owned files/directories are immutable to UID 1000, including rename,
    # unlink and helper bytecode writes; only the separate /work is writable.
    for path in root.rglob("*"):
        if path.is_dir():
            path.chmod(0o555)
    root.chmod(0o555)
    return str(root / entrypoint), str(root)


def execute(request):
    language = request["language"]
    if language not in ("python", "posix"):
        raise ValueError("unsupported language")
    timeout = float(request["timeout_ms"]) / 1000
    limit = int(request["max_output_bytes"])
    if not 0.1 <= timeout <= 30 or not 1 <= limit <= 1024 * 1024:
        raise ValueError("resource limit outside policy")
    data = decode_bytes(request["input"], 256 * 1024)
    entrypoint, cwd = prepare_inputs(request)
    with open("/input/stdin", "xb") as target:
        target.write(data)
    os.chmod("/input/stdin", 0o400)
    if language == "python" and cwd == "/input/package":
        # -I keeps host/user environment and site paths excluded. Only the
        # approved package and its entrypoint directory join the module path.
        launcher = ("import os,runpy,sys; p=sys.argv[1]; "
                    "sys.path[:0]=[os.path.dirname(p),'/input/package']; "
                    "sys.argv=[p]; runpy.run_path(p,run_name='__main__')")
        command = ["/usr/bin/python3", "-I", "-B", "-c", launcher, entrypoint]
    else:
        command = (["/usr/bin/python3", "-I", "-B", entrypoint]
                   if language == "python" else ["/bin/sh", entrypoint])
    output = [bytearray(), bytearray()]
    reason = "completed"
    with open("/input/stdin", "rb") as source:
        child = subprocess.Popen(
            command, stdin=source, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            cwd=cwd, env={"PATH": "/usr/bin:/bin", "HOME": "/work", "LANG": "C.UTF-8"},
            preexec_fn=restrict_child, close_fds=True,
        )
    print("STUDIO_STARTED", flush=True)
    selector = selectors.DefaultSelector()
    selector.register(child.stdout, selectors.EVENT_READ, 0)
    selector.register(child.stderr, selectors.EVENT_READ, 1)
    deadline = time.monotonic() + timeout
    try:
        while selector.get_map():
            if time.monotonic() >= deadline:
                reason = "timeout"
                break
            for key, _ in selector.select(0.05):
                chunk = os.read(key.fd, 4096)
                if not chunk:
                    selector.unregister(key.fileobj)
                    continue
                remaining = limit - sum(map(len, output))
                output[key.data].extend(chunk[:remaining])
                if len(chunk) > remaining:
                    reason = "output_limit"
                    break
            if reason != "completed":
                break
        # A child can close output and keep running. It still has the same deadline.
        if reason == "completed":
            try:
                child.wait(timeout=max(0.001, deadline - time.monotonic()))
            except subprocess.TimeoutExpired:
                reason = "timeout"
    finally:
        # Also kill descendants that inherited pipes; the VM is destroyed afterward.
        try:
            os.killpg(child.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        child.wait()
        selector.close()
        child.stdout.close()
        child.stderr.close()
    return {
        "stdout": base64.b64encode(output[0]).decode("ascii"),
        "stderr": base64.b64encode(output[1]).decode("ascii"),
        "exit_code": child.returncode,
        "reason": reason,
    }


print("STUDIO_READY", flush=True)
try:
    with open("/request.json", "rb") as source:
        raw = source.read(MAX_REQUEST + 1)
    if len(raw) > MAX_REQUEST:
        raise ValueError("invalid request framing")
    result = execute(json.loads(raw))
    print("STUDIO_RESULT:" + json.dumps(result, separators=(",", ":")), flush=True)
except json.JSONDecodeError as error:
    print("STUDIO_ERROR:invalid_json:length=" + str(len(raw)) + ":offset=" + str(error.pos), flush=True)
except Exception as error:
    print("STUDIO_ERROR:" + type(error).__name__, flush=True)
finally:
    subprocess.run(["/bin/busybox", "poweroff", "-f"], check=False)
    while True:
        time.sleep(1)
