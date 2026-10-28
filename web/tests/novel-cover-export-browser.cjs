const { chromium, expect } = require("@playwright/test");
const fs = require("node:fs/promises");
(async () => {
  const [origin, title, book, cover, other, scene] = process.argv.slice(2);
  if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(origin))
    throw new Error("isolated loopback required");
  const browser = await chromium.launch();
  try {
    const page = await browser.newPage({
      viewport: { width: 1440, height: 1000 },
    });
    page.setDefaultTimeout(12000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.goto(origin);
    await page.getByLabel("用户名", { exact: true }).fill("owner");
    await page
      .getByLabel("密码", { exact: true })
      .fill("test-owner-password-123");
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await page
      .getByRole("button", {
        name: `${title} · 继续完善世界与故事`,
        exact: true,
      })
      .click();
    await page
      .getByRole("button", { name: "整书与分卷导出", exact: true })
      .click();
    const choice = page.getByLabel("导出封面版本", { exact: true });
    await expect(choice.locator(`option[value="${cover}"]`)).toHaveCount(1);
    await expect(choice.locator(`option[value="${scene}"]`)).toHaveCount(0);
    await expect(choice).toHaveValue("");
    await page.getByLabel("导出作者署名", { exact: true }).fill("测试作者");
    await choice.selectOption(cover);
    const preview = page.getByRole("region", {
      name: "导出内容预览",
      exact: true,
    });
    await expect(preview).toHaveCount(0);
    await page
      .getByRole("button", { name: "检查导出内容", exact: true })
      .click();
    const image = preview.getByRole("img", {
      name: "本次导出的已采用封面",
      exact: true,
    });
    await expect(image).toBeVisible();
    await expect
      .poll(() => image.evaluate((e) => e.complete && e.naturalWidth === 3))
      .toBe(true);
    await page
      .getByLabel("我已核对缺章与提示，导出当前可用稿，并在书籍信息中保留说明")
      .check();
    const pdfButton = page.getByRole("button", {
      name: "下载 PDF 排版稿",
      exact: true,
    });
    await expect(pdfButton).toBeEnabled();
    await choice.selectOption(other);
    await expect(preview).toHaveCount(0);
    await choice.selectOption(cover);
    await page
      .getByRole("button", { name: "检查导出内容", exact: true })
      .click();
    await page
      .getByLabel("我已核对缺章与提示，导出当前可用稿，并在书籍信息中保留说明")
      .check();
    for (const [button, format] of [
      ["下载 PDF 排版稿", "pdf"],
      ["下载 EPUB 电子书", "epub"],
    ]) {
      const pending = page.waitForEvent("download");
      await page.getByRole("button", { name: button, exact: true }).click();
      const file = await pending;
      const data = await fs.readFile(await file.path());
      expect(data.subarray(0, format === "pdf" ? 5 : 2).toString()).toBe(
        format === "pdf" ? "%PDF-" : "PK",
      );
      if (format === "pdf")
        expect(data.includes(Buffer.from("/Subtype /Image"))).toBe(true);
      if (process.env.STUDIO_NOVEL_COVER_OUTPUT)
        await file.saveAs(`${process.env.STUDIO_NOVEL_COVER_OUTPUT}.${format}`);
    }
    if (process.env.STUDIO_NOVEL_COVER_SCREENSHOT)
      await page.screenshot({
        path: process.env.STUDIO_NOVEL_COVER_SCREENSHOT,
        fullPage: true,
      });
    expect(errors).toEqual([]);
    console.log(
      "Real Vue/HTTP cover selection, original WebP pixels, adoption filter, preview invalidation and PDF/EPUB downloads passed; no model calls.",
    );
  } finally {
    await browser.close();
  }
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
