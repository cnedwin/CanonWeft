"""Actual Windows parser, OCR and OS-limit tests. No database or provider calls."""
import argparse
import ctypes
from ctypes import wintypes
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))
import worker
from limits import Job
sys.path.insert(0, str(HERE))
from make_fixtures import native_pdf

FIXTURES = HERE / "fixtures"


def arguments(name, ocr="enabled"):
    return argparse.Namespace(source=str(FIXTURES / name), extension=Path(name).suffix, ocr=ocr)


def run_document(name, ocr="enabled"):
    args = arguments(name, ocr)
    output = subprocess.check_output([sys.executable, "-B", "-I", str(HERE.parent / "worker.py"),
                                      "--source", args.source, "--extension", args.extension,
                                      "--ocr", ocr], timeout=135)
    return json.loads(output)


class RuntimeTests(unittest.TestCase):
    def test_resumable_plan_and_selected_page_keep_original_locator(self):
        args = arguments("mixed.pdf")
        def call(phase, page=0):
            output = subprocess.check_output([sys.executable, "-B", "-I", str(HERE.parent / "worker.py"),
                "--source", args.source, "--extension", args.extension, "--phase", phase,
                "--page", str(page), "--ocr", "enabled"], timeout=45)
            return json.loads(output)
        before = hashlib.sha256(Path(args.source).read_bytes()).digest()
        self.assertEqual(call("plan"), {"parser": worker.PARSER, "pages": 2})
        second = call("unit", 1)
        self.assertEqual(len(second["units"]), 1)
        self.assertEqual(second["units"][0]["locator"], "page:2")
        self.assertEqual(second["units"][0]["kind"], "ocr")
        self.assertEqual(second["units"][0]["status"], "extracted")
        self.assertIn("星河", second["units"][0]["text"])
        self.assertEqual(call("unit", -1), {"error": "invalid_unit"})
        self.assertEqual(call("unit", 2), {"error": "invalid_unit"})
        self.assertEqual(before, hashlib.sha256(Path(args.source).read_bytes()).digest())

    def test_alpha_compositing_after_exif_orientation(self):
        from PIL import Image
        picture = Image.new("RGBA", (2, 3), (0, 0, 0, 0))
        picture.putpixel((0, 0), (255, 0, 0, 255))
        picture.putpixel((0, 1), (0, 0, 0, 128))
        metadata = Image.Exif()
        metadata[274] = 6
        with tempfile.TemporaryDirectory() as folder:
            source = Path(folder) / "oriented.png"
            picture.save(source, exif=metadata)
            decoded = worker.checked_image(source)
            self.assertEqual(decoded.size, (3, 2))
            self.assertEqual(decoded.getpixel((2, 0)), (255, 0, 0))
            self.assertEqual(decoded.getpixel((1, 0)), (127, 127, 127))
            self.assertEqual(decoded.getpixel((0, 0)), (255, 255, 255))

    def test_alpha_visible_text_and_original_bytes(self):
        for name in ["alpha-visible.png", "alpha-visible.webp", "alpha-la.png", "alpha-palette.png"]:
            with self.subTest(name=name):
                before = hashlib.sha256((FIXTURES / name).read_bytes()).digest()
                decoded = worker.checked_image(FIXTURES / name)
                self.assertEqual(decoded.getpixel((0, 0)), (255, 255, 255))
                item = run_document(name)["units"][0]
                self.assertEqual(item["status"], "extracted")
                self.assertIn("星河", item["text"])
                self.assertIn("River", item["text"])
                self.assertEqual(before, hashlib.sha256((FIXTURES / name).read_bytes()).digest())

    def test_fully_transparent_rgb_is_not_recognized(self):
        for name in ["alpha-hidden.png", "alpha-hidden.webp"]:
            with self.subTest(name=name):
                before = hashlib.sha256((FIXTURES / name).read_bytes()).digest()
                decoded = worker.checked_image(FIXTURES / name)
                self.assertEqual(decoded.getextrema(), ((255, 255), (255, 255), (255, 255)))
                item = run_document(name)["units"][0]
                self.assertEqual(item["status"], "pending")
                self.assertEqual(item["reason"], "no_text_visual_reference")
                self.assertNotIn("text", item)
                self.assertEqual(before, hashlib.sha256((FIXTURES / name).read_bytes()).digest())

    def test_effective_ocr_preprocessing_bound(self):
        import numpy as np
        engine = worker.create_ocr_engine()
        # Exercise the actual global, padding and detector preprocessing chain above 1500 pixels.
        for height, width in [(700, 1800), (2600, 900), (1700, 2400)]:
            with self.subTest(shape=(height, width)):
                original = np.full((height, width, 3), 255, dtype=np.uint8)
                prepared, _, _ = engine.preprocess(original)
                prepared, _ = engine.maybe_add_letterbox(prepared, {})
                detection = engine.text_det.get_preprocess(max(prepared.shape[:2]))(prepared)
                self.assertLessEqual(max(prepared.shape[:2]), 1280)
                self.assertLessEqual(max(detection.shape[-2:]), 1280)
                self.assertLess(max(prepared.shape[:2]), max(original.shape[:2]))

    def test_native_and_real_ocr(self):
        for name in ["native.pdf", "scan.pdf", "mixed.pdf", "text.png", "text.jpg", "text.webp"]:
            with self.subTest(name=name):
                before = hashlib.sha256((FIXTURES / name).read_bytes()).digest()
                report = run_document(name)
                self.assertNotIn("error", report)
                self.assertEqual(len(report["units"]), 2 if name.endswith("pdf") else 1)
                for index, item in enumerate(report["units"], 1):
                    self.assertEqual(item["locator"], f"page:{index}" if name.endswith("pdf") else "image:1")
                    self.assertEqual(item["status"], "extracted")
                    self.assertIn("星河", item["text"])
                    self.assertIn("River", item["text"])
                    if item["kind"] == "ocr":
                        self.assertGreaterEqual(item["confidence"], 0.8)
                self.assertEqual(before, hashlib.sha256((FIXTURES / name).read_bytes()).digest())

    def test_disabled_ocr_per_page(self):
        units = run_document("mixed.pdf", "disabled")["units"]
        self.assertEqual([item["status"] for item in units], ["extracted", "pending"])
        self.assertEqual(units[1]["locator"], "page:2")
        self.assertEqual(units[1]["reason"], "ocr_disabled")

    def test_encrypted_and_limits(self):
        for name, code in [("encrypted.pdf", "encrypted"), ("encrypted-empty-password.pdf", "encrypted"),
                           ("too-many-pages.pdf", "page_limit"), ("oversize.png", "pixel_limit")]:
            with self.subTest(name=name):
                self.assertEqual(run_document(name), {"error": code})

    def test_blank_image_is_visual_reference(self):
        item = run_document("blank.png")["units"][0]
        self.assertEqual(item["status"], "pending")
        self.assertEqual(item["reason"], "no_text_visual_reference")
        self.assertNotIn("text", item)

    def test_missing_model_is_pending_not_empty_success(self):
        manifest = json.loads(worker.MANIFEST.read_text(encoding="utf-8"))
        manifest["models"][0]["sha256"] = "0" * 64
        with tempfile.TemporaryDirectory() as folder:
            bad = Path(folder) / "manifest.json"
            bad.write_text(json.dumps(manifest), encoding="utf-8")
            original = worker.MANIFEST
            try:
                worker.MANIFEST = bad
                result = worker.ocr_image(worker.checked_image(FIXTURES / "text.png"), "page:2", "enabled")
                self.assertEqual(result["reason"], "ocr_unavailable")
                self.assertEqual(result["status"], "pending")
            finally:
                worker.MANIFEST = original

    def test_file_deadline_keeps_every_page(self):
        original = worker.FILE_SECONDS
        try:
            worker.FILE_SECONDS = 0.001
            report = worker.coordinate(arguments("mixed.pdf"))
            self.assertEqual([item["locator"] for item in report["units"]], ["page:1", "page:2"])
            self.assertTrue(all(item["reason"] == "file_timeout" for item in report["units"]))
        finally:
            worker.FILE_SECONDS = original

    def test_real_scan_deadline_preserves_completed_native_page(self):
        original = worker.FILE_SECONDS
        try:
            worker.FILE_SECONDS = 1.1
            report = worker.coordinate(arguments("mixed.pdf"))
            self.assertEqual(report["units"][0]["status"], "extracted")
            self.assertIn("星河", report["units"][0]["text"])
            self.assertEqual(report["units"][1]["locator"], "page:2")
            self.assertEqual(report["units"][1]["reason"], "file_timeout")
        finally:
            worker.FILE_SECONDS = original

    def test_fixture_hashes(self):
        manifest = json.loads((HERE / "fixtures.json").read_text(encoding="utf-8"))
        for entry in manifest["files"]:
            data = (FIXTURES / entry["name"]).read_bytes()
            self.assertEqual(len(data), entry["bytes"])
            self.assertEqual(hashlib.sha256(data).hexdigest(), entry["sha256"])

    def test_actual_page_timeout(self):
        with Job() as job:
            started = time.monotonic()
            result = worker.run_child(job, arguments("scan.pdf"), "page", 0.4)
            self.assertEqual(result, {"error": "page_timeout"})
            self.assertLess(time.monotonic() - started, 3)

    def test_os_memory_limit(self):
        code = "input()\ntry:\n x=bytearray(256*1024*1024)\nexcept MemoryError:\n raise SystemExit(7)\n"
        with Job(memory_bytes=64 * 1024 * 1024) as job:
            process = subprocess.Popen([sys.executable, "-B", "-I", "-c", code], stdin=subprocess.PIPE,
                                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, creationflags=0x4)
            job.assign(process)
            try:
                process.communicate(b"go\n", timeout=5)
                self.assertEqual(process.returncode, 7)
            finally:
                job.terminate()
                process.wait(timeout=5)

    def test_os_cpu_limit(self):
        with Job(cpu_seconds=0.1) as job:
            # Windows periodically enforces user CPU limits, not a wall timer.
            # The real host probe observed quota exits at 1.09, 3.08 and 7.08s.
            # Verify the installed 100ms limit AND the OS quota exit, allowing
            # the periodic sweep to run; page/file wall deadlines stay unchanged.
            # https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-jobobject_basic_limit_information
            class Basic(ctypes.Structure):
                _fields_ = [("process_time", ctypes.c_int64), ("job_time", ctypes.c_int64),
                            ("flags", wintypes.DWORD), ("minimum", ctypes.c_size_t),
                            ("maximum", ctypes.c_size_t), ("active", wintypes.DWORD),
                            ("affinity", ctypes.c_size_t), ("priority", wintypes.DWORD),
                            ("scheduling", wintypes.DWORD)]
            info = Basic()
            job.api.QueryInformationJobObject.argtypes = [wintypes.HANDLE, ctypes.c_int, ctypes.c_void_p,
                                                          wintypes.DWORD, ctypes.c_void_p]
            self.assertTrue(job.api.QueryInformationJobObject(job.handle, 2, ctypes.byref(info),
                                                              ctypes.sizeof(info), None))
            self.assertEqual(info.process_time, 1_000_000)
            self.assertTrue(info.flags & 0x2)
            process = subprocess.Popen([sys.executable, "-B", "-I", "-c", "input()\nwhile True: pass"],
                                       stdin=subprocess.PIPE, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, creationflags=0x4)
            try:
                job.assign(process)
                process.communicate(b"go\n", timeout=15)
                self.assertEqual(process.returncode, 0xC0000044)  # STATUS_QUOTA_EXCEEDED
            finally:
                job.terminate()
                process.wait(timeout=5)

    def test_coordinator_kill_cleans_child(self):
        code = (f"import sys;sys.path.insert(0,{str(HERE.parent)!r});from limits import Job;import subprocess;"
                "job=Job();p=subprocess.Popen([sys.executable,'-B','-I','-c','import time;time.sleep(60)'],creationflags=0x4);"
                "job.assign(p);print(p.pid,flush=True);input()")
        coordinator = subprocess.Popen([sys._base_executable, "-B", "-I", "-c", code], stdin=subprocess.PIPE, stdout=subprocess.PIPE)
        child_pid = int(coordinator.stdout.readline())
        api = ctypes.WinDLL("kernel32", use_last_error=True)
        api.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
        api.OpenProcess.restype = wintypes.HANDLE
        api.WaitForSingleObject.argtypes = [wintypes.HANDLE, wintypes.DWORD]
        api.CloseHandle.argtypes = [wintypes.HANDLE]
        handle = api.OpenProcess(0x100000, False, child_pid)
        self.assertTrue(handle)
        try:
            coordinator.kill()
            coordinator.communicate(timeout=5)
            self.assertEqual(api.WaitForSingleObject(handle, 5000), 0)
        finally:
            api.CloseHandle(handle)

    def test_network_guard(self):
        code = (f"import sys;sys.path.insert(0,{str(HERE.parent)!r});import worker,socket;worker.deny_network();"
                "socket.create_connection(('example.com',443))")
        result = subprocess.run([sys.executable, "-B", "-I", "-c", code], capture_output=True, timeout=5)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(b"ingest_network_disabled", result.stderr)

    def test_pdf_external_action_is_not_fetched(self):
        # Separate process: worker.coordinate installs an intentionally irreversible audit hook.
        with tempfile.TemporaryDirectory() as folder:
            log = Path(folder) / "requests.json"
            trap = subprocess.Popen([sys._base_executable, "-B", "-I", str(HERE / "http_trap.py"), str(log)],
                                    stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
            try:
                port = int(trap.stdout.readline())
                probe = f"import urllib.request;urllib.request.urlopen('http://127.0.0.1:{port}/probe').read()"
                subprocess.run([sys._base_executable, "-B", "-I", "-c", probe], check=True, timeout=5)
                action = f"/OpenAction << /S /URI /URI (http://127.0.0.1:{port}/attachment) >>".encode()
                source = Path(folder) / "external.pdf"
                source.write_bytes(native_pdf(action=action))
                args = argparse.Namespace(source=str(source), extension=".pdf", ocr="enabled")
                report = worker.coordinate(args)
                self.assertTrue(all(item["status"] == "extracted" for item in report["units"]))
                self.assertEqual(json.loads(log.read_text()), ["/probe"])
            finally:
                trap.kill()
                trap.communicate(timeout=5)

    def test_text_and_render_limits_preserve_page_anchors(self):
        with tempfile.TemporaryDirectory() as folder:
            source = Path(folder) / "large.pdf"
            source.write_bytes(native_pdf(content_override=b"BT /F1 0.0001 Tf 30 200 Td " + (b"(" + b"x" * 10000 + b") Tj ") * 27 + b"ET"))
            args = argparse.Namespace(source=str(source), extension=".pdf", ocr="enabled")
            report = worker.coordinate(args)
            self.assertEqual([item["locator"] for item in report["units"]], ["page:1", "page:2"])
            self.assertTrue(all(item["reason"] == "text_limit" for item in report["units"]))
            source.write_bytes(native_pdf(content_override=b"", media_box="0 0 20000 20000"))
            report = worker.coordinate(args)
            self.assertTrue(all(item["reason"] == "pixel_limit" for item in report["units"]))


if __name__ == "__main__":
    unittest.main(verbosity=2)
