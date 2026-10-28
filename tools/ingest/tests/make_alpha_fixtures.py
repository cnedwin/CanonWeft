"""Authored transparency regression fixtures; preserve hidden RGB intentionally."""
from pathlib import Path
from PIL import Image, ImageDraw, ImageFont

folder = Path(__file__).parent / "fixtures"
font = ImageFont.truetype("C:/Windows/Fonts/msyh.ttc", 48)
visible = Image.new("RGBA", (1200, 500), (0, 0, 0, 0))
draw = ImageDraw.Draw(visible)
draw.text((70, 100), "星河档案 River archive", font=font, fill=(0, 0, 0, 255))
draw.text((70, 210), "第二章 Chapter 2", font=font, fill=(0, 0, 0, 255))
hidden = Image.open(folder / "text.png").convert("RGBA")
hidden.putalpha(0)
for name, picture in [("alpha-visible", visible), ("alpha-hidden", hidden)]:
    picture.save(folder / f"{name}.png")
    picture.save(folder / f"{name}.webp", lossless=True, exact=True)
visible.convert("LA").save(folder / "alpha-la.png")
palette = visible.getchannel("A").point(lambda value: 1 if value >= 128 else 0).convert("P")
palette.putpalette([0, 0, 0] * 256)
palette.info["transparency"] = bytes([0, 255] + [255] * 254)
palette.save(folder / "alpha-palette.png")
