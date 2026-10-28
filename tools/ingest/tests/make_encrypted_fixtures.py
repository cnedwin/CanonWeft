"""Developer-only fixture generator, using bundled pypdf 6.10.0; no runtime dependency."""
from pathlib import Path
import pypdf

folder = Path(__file__).parent / "fixtures"
for name, password in [("encrypted.pdf", "fixture-password"), ("encrypted-empty-password.pdf", "")]:
    writer = pypdf.PdfWriter()
    writer.append_pages_from_reader(pypdf.PdfReader(folder / "native.pdf"))
    writer.encrypt(password)
    writer.write(folder / name)
