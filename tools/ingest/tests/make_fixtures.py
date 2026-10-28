"""Author tiny, copyright-free fixtures. No downloaded documents or fonts bundled."""
from pathlib import Path
from PIL import Image, ImageDraw, ImageFont
import pypdfium2 as pdfium

ROOT = Path(__file__).parent / "fixtures"

def native_pdf(pages=2, action=b"", content_override=None, media_box="0 0 400 300"):
    # Embedded ToUnicode maps the CIDs to authored Chinese, without embedding a font.
    cmap = b"/CIDInit /ProcSet findresource begin 12 dict begin begincmap /CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def /CMapName /Test def /CMapType 2 def 1 begincodespacerange <0000> <FFFF> endcodespacerange 2 beginbfchar <0001> <661F> <0002> <6CB3> endbfchar endcmap CMapName currentdict /CMap defineresource pop end end"
    content = b"BT /F1 24 Tf 30 200 Td (River archive) Tj /F2 24 Tf 0 -40 Td <00010002> Tj ET"
    if content_override is not None:
        content = content_override
    objects = [b"<< /Type /Catalog /Pages 2 0 R " + action + b" >>", b"", b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>", b"<< /Type /Font /Subtype /Type0 /BaseFont /STSong-Light /Encoding /Identity-H /DescendantFonts [5 0 R] /ToUnicode 6 0 R >>", b"<< /Type /Font /Subtype /CIDFontType0 /BaseFont /STSong-Light /CIDSystemInfo << /Registry (Adobe) /Ordering (GB1) /Supplement 4 >> >>", b"<< /Length " + str(len(cmap)).encode() + b" >>\nstream\n" + cmap + b"\nendstream"]
    kids=[]
    for i in range(pages):
        number=len(objects)+1
        kids.append(f"{number} 0 R")
        objects.append(f"<< /Type /Page /Parent 2 0 R /MediaBox [{media_box}] /Resources << /Font << /F1 3 0 R /F2 4 0 R >> >> /Contents {number+1} 0 R >>".encode())
        objects.append(b"<< /Length " + str(len(content)).encode() + b" >>\nstream\n" + content + b"\nendstream")
    objects[1]=f"<< /Type /Pages /Count {pages} /Kids [{' '.join(kids)}] >>".encode()
    result=b"%PDF-1.4\n"
    offsets=[0]
    for i,obj in enumerate(objects,1):
        offsets.append(len(result)); result+=f"{i} 0 obj\n".encode()+obj+b"\nendobj\n"
    start=len(result)
    result+=f"xref\n0 {len(objects)+1}\n0000000000 65535 f \n".encode()
    result+=b"".join(f"{offset:010d} 00000 n \n".encode() for offset in offsets[1:])
    result+=f"trailer\n<< /Size {len(objects)+1} /Root 1 0 R >>\nstartxref\n{start}\n%%EOF".encode()
    return result

def generate():
    ROOT.mkdir(exist_ok=True)
    (ROOT / "native.pdf").write_bytes(native_pdf())
    (ROOT / "too-many-pages.pdf").write_bytes(native_pdf(101))
    font=ImageFont.truetype("C:/Windows/Fonts/msyh.ttc",48)
    picture=Image.new("RGB",(1200,500),"white")
    draw=ImageDraw.Draw(picture)
    draw.text((70,100),"星河档案 River archive",font=font,fill="black")
    draw.text((70,210),"第二章 Chapter 2",font=font,fill="black")
    for suffix in ["png","jpg","webp"]:
        picture.save(ROOT / f"text.{suffix}")
    picture.save(ROOT / "scan.pdf",save_all=True,append_images=[picture])
    blank=Image.new("RGB",(1200,500),"white")
    blank.save(ROOT / "blank.png")
    Image.new("1",(6000,6000)).save(ROOT / "oversize.png")
    with pdfium.PdfDocument(ROOT / "native.pdf") as native, pdfium.PdfDocument(ROOT / "scan.pdf") as scan:
        mixed=pdfium.PdfDocument.new()
        mixed.import_pages(native,[0]); mixed.import_pages(scan,[0]); mixed.save(ROOT / "mixed.pdf")
        mixed.close()
    print("Authored fixtures generated")


if __name__ == "__main__":
    generate()
