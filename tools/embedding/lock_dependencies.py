"""Record exact PyPI wheel hashes from a successful pip --report installation."""
import json
from pathlib import Path
import sys

report = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
lines = ["# Verified Windows Python 3.12 wheels; install with --require-hashes."]
for item in sorted(report["install"], key=lambda row: row["metadata"]["name"].lower()):
    metadata = item["metadata"]
    digest = item["download_info"]["archive_info"]["hashes"]["sha256"]
    lines.append(f"{metadata['name']}=={metadata['version']} --hash=sha256:{digest}")
Path(sys.argv[2]).write_text("\n".join(lines) + "\n", encoding="utf-8")
