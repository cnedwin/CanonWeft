"""Authenticated loopback-only inference from pinned local ONNX weights."""
from __future__ import annotations

import argparse
import hashlib
import hmac
import json
import os
from pathlib import Path
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

os.environ["TOKENIZERS_PARALLELISM"] = "false"
os.environ["HF_HUB_OFFLINE"] = "1"
os.environ["HF_HUB_DISABLE_TELEMETRY"] = "1"

import numpy as np
import onnxruntime as ort
from tokenizers import Tokenizer


class EmbeddingEngine:
    def __init__(self, model: Path, manifest: Path):
        self.manifest = json.loads(manifest.read_text(encoding="utf-8"))
        required = {"model.onnx", "model.onnx_data", "Constant_7_attr__value", "config.json", "tokenizer.json"}
        if (self.manifest["model"] != "BAAI/bge-m3" or self.manifest["dimension"] != 1024
                or self.manifest["revision"] != "5617a9f61b028005a4858fdac845db406aefb181"
                or set(self.manifest["files"]) != required):
            raise ValueError("Unsupported embedding model")
        for name, expected in self.manifest["files"].items():
            path = model / name
            if path.resolve().parent != model.resolve() or path.stat().st_size != expected["size"]:
                raise ValueError("Embedding file scope or size mismatch")
            with path.open("rb") as stream:
                actual = hashlib.file_digest(stream, "sha256").hexdigest()
            if actual != expected["sha256"]:
                raise ValueError("Embedding checksum mismatch")
        ort.disable_telemetry_events()
        options = ort.SessionOptions()
        options.intra_op_num_threads = 8
        options.inter_op_num_threads = 1
        options.log_severity_level = 3
        self.session = ort.InferenceSession(str(model / "model.onnx"), sess_options=options,
                                            providers=["CPUExecutionProvider"])
        self.tokenizer = Tokenizer.from_file(str(model / "tokenizer.json"))
        self.tokenizer.no_truncation()
        self.tokenizer.no_padding()
        config = json.loads((model / "config.json").read_text(encoding="utf-8"))
        self.pad_id = config["pad_token_id"]
        self.lock = threading.Lock()

    def encode(self, texts: list[str]) -> dict:
        if not isinstance(texts, list) or not 1 <= len(texts) <= 8:
            raise ValueError("Expected one to eight texts")
        if any(not isinstance(t, str) or not t.strip() or len(t.encode("utf-8")) > 32768 for t in texts):
            raise ValueError("Text is empty or exceeds input limit")
        encoded = self.tokenizer.encode_batch(texts)
        lengths = [len(item.ids) for item in encoded]
        if max(lengths) > 2048:
            raise ValueError("Text exceeds 2048-token batch limit; split before embedding")
        ids = np.full((len(texts), max(lengths)), self.pad_id, dtype=np.int64)
        mask = np.zeros_like(ids)
        for index, item in enumerate(encoded):
            ids[index, :len(item.ids)] = item.ids
            mask[index, :len(item.ids)] = 1
        feeds = {}
        for input_spec in self.session.get_inputs():
            if input_spec.name == "input_ids":
                feeds[input_spec.name] = ids
            elif input_spec.name == "attention_mask":
                feeds[input_spec.name] = mask
            elif input_spec.name == "token_type_ids":
                feeds[input_spec.name] = np.zeros_like(ids)
            else:
                raise ValueError("Unexpected ONNX input contract")
        started = time.monotonic()
        if not self.lock.acquire(timeout=30):
            raise TimeoutError("Embedding worker busy")
        try:
            output = self.session.run(None, feeds)[0]
        finally:
            self.lock.release()
        # BGE-M3 dense retrieval uses the normalized CLS hidden state.
        vectors = output[:, 0, :] if output.ndim == 3 else output
        if vectors.shape != (len(texts), 1024) or not np.isfinite(vectors).all():
            raise ValueError("Embedding shape or values invalid")
        norms = np.linalg.norm(vectors, axis=1, keepdims=True)
        if (norms <= 0).any():
            raise ValueError("Empty embedding")
        vectors = (vectors / norms).astype(np.float32)
        return {"model": "BAAI/bge-m3", "revision": self.manifest["revision"], "dimension": 1024,
                "vectors": vectors.tolist(), "text_hashes": [hashlib.sha256(t.encode("utf-8")).hexdigest() for t in texts],
                "token_counts": lengths, "elapsed_ms": round((time.monotonic() - started) * 1000)}


def serve(engine: EmbeddingEngine, port: int, token: str) -> None:
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass  # Never log supplied text or authentication headers.

        def setup(self):
            super().setup()
            self.connection.settimeout(30)

        def respond(self, status: int, data: dict):
            raw = json.dumps(data, ensure_ascii=False, allow_nan=False).encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "application/json; charset=utf-8")
            self.send_header("Content-Length", str(len(raw)))
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            self.wfile.write(raw)

        def authorized(self) -> bool:
            return (self.headers.get("Host") == f"127.0.0.1:{port}"
                    and not self.headers.get("Origin")
                    and hmac.compare_digest(self.headers.get("Authorization", ""), f"Bearer {token}"))

        def do_GET(self):
            if not self.authorized():
                return self.respond(403, {"error": "denied"})
            if self.path != "/health":
                return self.respond(404, {"error": "not found"})
            self.respond(200, {"model": "BAAI/bge-m3", "revision": engine.manifest["revision"], "dimension": 1024})

        def do_POST(self):
            if not self.authorized():
                return self.respond(403, {"error": "denied"})
            if self.path != "/embed":
                return self.respond(404, {"error": "not found"})
            try:
                size = int(self.headers.get("Content-Length", "0"))
                if not 0 < size <= 262144:
                    return self.respond(413, {"error": "input size limit"})
                request = json.loads(self.rfile.read(size))
                if set(request) != {"texts"}:
                    raise ValueError("Unexpected request")
                result = engine.encode(request["texts"])
            except TimeoutError:
                return self.respond(503, {"error": "worker busy"})
            except (ValueError, TypeError, KeyError):
                return self.respond(400, {"error": "invalid embedding input"})
            except Exception:
                return self.respond(503, {"error": "embedding failed"})
            self.respond(200, result)

    server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    server.daemon_threads = True
    print(f"BGE-M3 ready on loopback:{port}", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--model", type=Path, required=True)
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--port", type=int, default=18766)
    args = parser.parse_args()
    secret = os.environ.get("STUDIO_EMBEDDING_TOKEN", "")
    if len(secret) < 32:
        raise SystemExit("Embedding authentication is required")
    serve(EmbeddingEngine(args.model, args.manifest), args.port, secret)
