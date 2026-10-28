"""Offline file extraction. Coordinator never decodes documents; children are bounded."""
import argparse
import hashlib
import importlib.metadata
import json
import math
import os
from pathlib import Path
import subprocess
import sys
import time

# Must be set before native libraries load; cap BLAS as well as ONNX thread pools.
os.environ["OMP_NUM_THREADS"] = "2"
os.environ["OPENBLAS_NUM_THREADS"] = "2"

MAX_INPUT = 32 * 1024 * 1024
MAX_TEXT = 256 * 1024
MAX_PIXELS = 25_000_000
MAX_PAGES = 100
MAX_SIDE = 12_000
PAGE_SECONDS = 30
FILE_SECONDS = 40
PARSER = "pdfium-5.14.0+rapidocr-1.4.4-cpu-v2"
HERE = Path(__file__).resolve().parent
MANIFEST = HERE.parent.parent / "deploy" / "locks" / "ingest.json"


class Rejected(Exception):
    pass


def deny_network():
    # All parser inputs are bytes/PIL images, never URLs; block Python networking as defense in depth.
    def audit(event, _args):
        if event in {"socket.connect", "socket.connect_ex", "socket.getaddrinfo", "socket.bind"}:
            raise PermissionError("ingest_network_disabled")
    sys.addaudithook(audit)


def verify_installation(ocr=True):
    manifest = json.loads(MANIFEST.read_text(encoding="utf-8"))
    for name in (["pypdfium2", "pillow"] + (["rapidocr-onnxruntime", "onnxruntime"] if ocr else [])):
        try:
            installed = importlib.metadata.version(name)
        except importlib.metadata.PackageNotFoundError:
            raise Rejected("runtime_unavailable") from None
        if installed != manifest["versions"][name]:
            raise Rejected("runtime_unavailable")
    if ocr:
        location = Path(importlib.metadata.distribution("rapidocr-onnxruntime").locate_file("rapidocr_onnxruntime/models"))
        for model in manifest["models"]:
            if hashlib.sha256((location / model["name"]).read_bytes()).hexdigest() != model["sha256"]:
                raise Rejected("ocr_unavailable")


def open_pdf(source):
    import pypdfium2 as pdfium
    try:
        document = pdfium.PdfDocument(source)
    except pdfium.PdfiumError as error:
        raise Rejected("encrypted" if getattr(error, "err_code", None) == 4 else "corrupt") from None
    # Even an empty user password must not bypass explicit encrypted-file rejection.
    if pdfium.raw.FPDF_GetSecurityHandlerRevision(document) >= 0:
        document.close()
        raise Rejected("encrypted")
    return document


def checked_image(source):
    from PIL import Image, ImageOps
    Image.MAX_IMAGE_PIXELS = MAX_PIXELS
    try:
        picture = Image.open(source)
        if picture.format not in {"PNG", "JPEG", "WEBP"}:
            raise Rejected("corrupt")
        width, height = picture.size
        if width * height > MAX_PIXELS or max(width, height) > MAX_SIDE:
            raise Rejected("pixel_limit")
        if getattr(picture, "n_frames", 1) != 1:
            raise Rejected("animated_image")
        picture.load()
        oriented = ImageOps.exif_transpose(picture)
        # OCR sees the document as rendered on white, never hidden RGB under alpha=0.
        # Conversion to RGBA also applies LA and PNG palette/color-key transparency.
        if "A" in oriented.getbands() or "transparency" in oriented.info:
            rgba = oriented.convert("RGBA")
            background = Image.new("RGBA", rgba.size, (255, 255, 255, 255))
            return Image.alpha_composite(background, rgba).convert("RGB")
        return oriented.convert("RGB")
    except (Image.DecompressionBombError, Image.DecompressionBombWarning):
        raise Rejected("pixel_limit") from None


def unit(locator, kind, text="", reason="", confidence=None):
    if len(text.encode("utf-8")) > MAX_TEXT:
        return {"locator": locator, "kind": kind, "status": "pending", "reason": "text_limit"}
    result = {"locator": locator, "kind": kind, "status": "extracted" if text.strip() and not reason else "pending"}
    if text:
        result["text"] = text
    if reason:
        result["reason"] = reason
    if confidence is not None:
        result["confidence"] = round(confidence, 4)
    return result


def create_ocr_engine():
    from rapidocr_onnxruntime import RapidOCR
    # RapidOCR 1.4.4 ignores det_limit_side_len in max mode. Bound its upstream
    # image instead, so detector AND recognition crops use at most 1280 pixels.
    # Disable minimum-side enlargement, which could defeat the global bound.
    return RapidOCR(intra_op_num_threads=2, inter_op_num_threads=1, text_score=0.0,
                    max_side_len=1280, min_side_len=1, det_limit_type="max",
                    det_use_cuda=False, rec_use_cuda=False, cls_use_cuda=False,
                    det_use_dml=False, rec_use_dml=False, cls_use_dml=False)


def ocr_image(picture, locator, mode):
    if mode == "disabled":
        return unit(locator, "ocr", reason="ocr_disabled")
    try:
        verify_installation()
        engine = create_ocr_engine()
    except (ImportError, FileNotFoundError, Rejected, importlib.metadata.PackageNotFoundError):
        return unit(locator, "ocr", reason="ocr_unavailable")
    try:
        lines, _ = engine(picture, text_score=0.0)
    except Exception:
        return unit(locator, "ocr", reason="ocr_resource_or_failure")
    if not lines:
        return unit(locator, "ocr", reason="no_text_visual_reference")
    text = "\n".join(str(line[1]) for line in lines)
    if not text.strip():
        return unit(locator, "ocr", reason="no_text_visual_reference")
    confidence = min(float(line[2]) for line in lines)
    return unit(locator, "ocr", text, "low_confidence" if confidence < 0.80 else "", confidence)


def extract_page(source, extension, page_number, mode):
    locator = f"page:{page_number + 1}" if extension == ".pdf" else "image:1"
    if extension != ".pdf":
        return ocr_image(checked_image(source), locator, mode)
    with open_pdf(source) as document:
        page = document[page_number]
        try:
            textpage = page.get_textpage()
            try:
                if textpage.count_chars() > MAX_TEXT:
                    return unit(locator, "pdf_text", reason="text_limit")
                text = textpage.get_text_range().strip()
            finally:
                textpage.close()
            if text:
                return unit(locator, "pdf_text", text)
            if mode == "disabled":
                return unit(locator, "ocr", reason="ocr_disabled")
            width, height = page.get_size()
            scale = 2.0  # 144 DPI. Reject rather than silently downsample huge pages.
            if not all(math.isfinite(value) and value > 0 for value in [width, height]):
                return unit(locator, "ocr", reason="page_unreadable")
            if math.ceil(width * scale) * math.ceil(height * scale) > MAX_PIXELS or max(width, height) * scale > MAX_SIDE:
                return unit(locator, "ocr", reason="pixel_limit")
            bitmap = page.render(scale=scale)
            try:
                return ocr_image(bitmap.to_pil().convert("RGB"), locator, mode)
            finally:
                bitmap.close()
        finally:
            page.close()


def child(args):
    # Parent assigns Job limits BEFORE allowing parsing/import of native libraries.
    if sys.stdin.buffer.readline() != b"go\n":
        return {"error": "runtime_unavailable"}
    deny_network()
    try:
        verify_installation(ocr=False)
        if args.phase == "inspect":
            if args.extension == ".pdf":
                with open_pdf(args.source) as document:
                    count = len(document)
                    if count < 1:
                        raise Rejected("corrupt")
                    if count > MAX_PAGES:
                        raise Rejected("page_limit")
                    return {"pages": count}
            checked_image(args.source)
            return {"pages": 1}
        return extract_page(args.source, args.extension, args.page, args.ocr)
    except Rejected as error:
        return {"error": str(error)}
    except MemoryError:
        return {"error": "resource_limit"}
    except Exception:
        # Parser messages may contain source text and paths; never emit them.
        return {"error": "corrupt"}


def run_child(job, args, phase, timeout, page=0):
    command = [sys.executable, "-B", "-I", str(HERE / "worker.py"), "--phase", phase,
               "--source", args.source, "--extension", args.extension, "--page", str(page), "--ocr", args.ocr]
    process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                               stderr=subprocess.DEVNULL, creationflags=subprocess.CREATE_NO_WINDOW | 0x4)
    try:
        job.assign(process)
        output, _ = process.communicate(b"go\n", timeout=timeout)
        if process.returncode != 0:
            return {"error": "resource_limit"}
        if len(output) > 2 * MAX_TEXT:
            return {"error": "text_limit"}
        return json.loads(output)
    except subprocess.TimeoutExpired:
        job.terminate()
        process.communicate()
        return {"error": "page_timeout"}
    finally:
        if process.poll() is None:
            job.terminate()
            process.wait()


def coordinate(args):
    # Isolated mode omits script directory from sys.path; add this fixed trusted directory only.
    sys.path.insert(0, str(HERE))
    from limits import Job
    deny_network()
    source = Path(args.source)
    if not source.is_file() or not 0 < source.stat().st_size <= MAX_INPUT:
        return {"error": "input_limit"}
    started = time.monotonic()
    with Job() as job:
        inspection = run_child(job, args, "inspect", PAGE_SECONDS)
        if "error" in inspection:
            return inspection
        phase = getattr(args, "phase", "file")
        if phase == "plan":
            return {"parser": PARSER, "pages": inspection["pages"]}
        if phase == "unit" and not 0 <= args.page < inspection["pages"]:
            return {"error": "invalid_unit"}
        units = []
        used = 0
        pages = [args.page] if phase == "unit" else range(inspection["pages"])
        for page in pages:
            locator = f"page:{page + 1}" if args.extension == ".pdf" else "image:1"
            remaining = FILE_SECONDS - (time.monotonic() - started)
            if remaining <= 0:
                result = unit(locator, "unparsed", reason="file_timeout")
            elif used >= MAX_TEXT:
                result = unit(locator, "unparsed", reason="text_limit")
            else:
                result = run_child(job, args, "page", min(PAGE_SECONDS, remaining), page)
                if result.get("error") == "page_timeout" and remaining < PAGE_SECONDS:
                    result["error"] = "file_timeout"
                if "error" in result:
                    result = unit(locator, "unparsed", reason=result["error"])
                size = len(result.get("text", "").encode("utf-8")) + (2 if used else 0)
                if used + size > MAX_TEXT:
                    result = unit(locator, result["kind"], reason="text_limit")
                    used = MAX_TEXT
                else:
                    used += size
            units.append(result)
        return {"parser": PARSER, "units": units}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--phase", choices=["file", "plan", "unit", "inspect", "page"], default="file")
    parser.add_argument("--source", required=True)
    parser.add_argument("--extension", choices=[".pdf", ".png", ".jpg", ".jpeg", ".webp"], required=True)
    parser.add_argument("--page", type=int, default=0)
    parser.add_argument("--ocr", choices=["enabled", "disabled"], default="enabled")
    args = parser.parse_args()
    try:
        result = coordinate(args) if args.phase in {"file", "plan", "unit"} else child(args)
    except Exception:
        result = {"error": "runtime_unavailable"}
    sys.stdout.buffer.write(json.dumps(result, ensure_ascii=False, separators=(",", ":")).encode("utf-8"))


if __name__ == "__main__":
    main()
