"""Independent extraction and rendering check for TestNovelPDFChinesePagination.

Uses the development document runtime; these packages are not app dependencies.
"""
import json
import re
import sys
from pathlib import Path

import pdfplumber
import pypdfium2
from pypdf import PdfReader

source = Path(sys.argv[1]).resolve()
out = Path(sys.argv[2]).resolve()
out.mkdir(parents=True, exist_ok=True)
reader = PdfReader(source)
long_book = "--long" in sys.argv[3:]
assert len(reader.pages) >= 5, "long manuscript was not paginated"
assert reader.metadata.title == ("长篇排版验证" if long_book else "雾城来信：雨夜与旧案")
assert reader.metadata.author == "测试作者"
# The renderer writes page numbers first in each content stream. Exclude only
# that anchored footer before joining a sentence continued on the next page.
text = "\n".join(re.sub(r"^第\s+\d+\s+页\s*", "", page.extract_text()) for page in reader.pages)
compact = re.sub(r"\s+", "", text)
if long_book:
    assert compact.count("他从城门出发，沿河堤寻找当年的证人。") == 120 * 125
    for i in range(1, 121):
        for label in ["章节起点", "章节终点"]:
            assert compact.count(f"{label}{i:03d}。") == 1, (i, label)
else:
    assert compact.count("林舟停在药铺门外，听见雨滴落进铜盆。") == 90
    assert compact.count("“你为什么回来？”她问。林舟没有回答，只把信放在桌上。") == 90
    for marker in ["首章终点甲。", "第二章起点乙。", "全书终点丙。", "<script>&42"]:
        assert marker in compact, f"missing text: {marker}"
    assert compact.count("KeepThisLongEnglishWordIntactWherePossible") == 12
assert "\ufffd" not in text and "\x00" not in text
links = sum(len(page.get("/Annots", [])) for page in reader.pages)
assert links >= (120 if long_book else 2), "missing clickable chapter contents"
for page in reader.pages:
    fonts = page["/Resources"]["/Font"].get_object()
    assert fonts, "missing font resources"
    for font in fonts.values():
        font = font.get_object()
        assert "/ToUnicode" in font, "font text cannot be mapped back to Unicode"
        descendant = font["/DescendantFonts"][0].get_object()
        assert "/FontFile2" in descendant["/FontDescriptor"].get_object()
with pdfplumber.open(source) as layout:
    for i, page in enumerate(layout.pages):
        for char in page.chars:
            assert char["x0"] >= 53 and char["x1"] <= page.width - 53, (i, char)
            assert char["top"] >= 40 and char["bottom"] <= page.height - 14, (i, char)
pdf = pypdfium2.PdfDocument(source)
images = []
for i in sorted({0, 1, 2, 3, len(pdf) - 1}):
    page = pdf[i]
    bitmap = page.render(scale=1.4)
    image = bitmap.to_pil()
    filename = out / f"page-{i + 1}.png"
    image.save(filename)
    images.append(str(filename))
    bitmap.close()
    page.close()
pdf.close()
print(json.dumps({"pages": len(reader.pages), "bytes": source.stat().st_size, "links": links, "textRoundTrip": True, "bounds": True, "rendered": images}, ensure_ascii=False))
