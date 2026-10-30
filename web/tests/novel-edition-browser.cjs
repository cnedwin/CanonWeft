const { chromium, expect } = require("@playwright/test");
const fs = require("node:fs/promises");
(async () => {
  const [origin, title, book, oldEdition] = process.argv.slice(2);
  if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(origin))
    throw new Error("isolated loopback required");
  const browser = await chromium.launch();
  try {
    const page = await browser.newPage({
      viewport: { width: 1440, height: 1000 },
    });
    page.setDefaultTimeout(15000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.goto(origin);
    await page.getByLabel("用户名", { exact: true }).fill("owner");
    await page
      .getByLabel("密码", { exact: true })
      .fill("test-owner-password-123");
    await page.getByRole("button", { name: "登录", exact: true }).click();
    async function enter() {
      await page
        .getByRole("button", {
          name: `${title} · 继续完善世界与故事`,
          exact: true,
        })
        .click();
      await page
        .getByRole("navigation", { name: "本书工作区" })
        .getByRole("button", { name: "审稿与导出", exact: true })
        .click();
      await page
        .getByRole("button", { name: "整书与分卷导出", exact: true })
        .click();
    }
    await enter();
    await page
      .getByLabel("导出正文来源", { exact: true })
      .selectOption("published");
    await page.getByLabel("导出作者署名", { exact: true }).fill("浏览器署名");
    await page.getByText("书籍信息与封面", { exact: true }).click();
    const cover = page.getByLabel("导出封面版本", { exact: true });
    await expect(
      cover.locator("option").filter({ hasText: "成书封面" }),
    ).toHaveCount(1);
    await cover.selectOption(
      await cover
        .locator("option")
        .filter({ hasText: "成书封面" })
        .getAttribute("value"),
    );
    await page
      .getByRole("button", { name: "检查导出内容", exact: true })
      .click();
    await expect(
      page.getByRole("region", { name: "导出内容预览", exact: true }),
    ).toBeVisible();
    const consent = page.getByLabel(
      "我已核对缺章与提示，导出当前可用稿，并在书籍信息中保留说明",
    );
    if (await consent.count()) await consent.check();
    await page.getByLabel("成书版本名称", { exact: true }).fill("浏览器成书版");
    await page
      .getByRole("button", { name: "确认保存成书版本", exact: true })
      .click();
    await expect(page.getByRole("status")).toContainText("成书版本已保存");
    await expect(page.getByLabel("导出正文来源", { exact: true })).toHaveValue(
      "edition",
    );
    await expect(page.getByLabel("导出作者署名", { exact: true })).toHaveCount(
      0,
    );
    await expect(
      page.getByLabel("历史书籍信息", { exact: true }),
    ).toContainText("浏览器署名");
    if (await consent.count()) await consent.check();
    const pending = page.waitForEvent("download");
    await page
      .getByRole("button", { name: "下载 Markdown 文稿", exact: true })
      .click();
    const downloaded = await pending;
    const md = await fs.readFile(await downloaded.path(), "utf8");
    expect(md).toContain(`# ${title}`);
    expect(md).toContain("浏览器署名");
    expect(md).toContain("药铺的灯熄灭了");
    if (process.env.STUDIO_NOVEL_EDITION_OUTPUT)
      await downloaded.saveAs(`${process.env.STUDIO_NOVEL_EDITION_OUTPUT}.md`);
    await page.reload();
    await enter();
    await page
      .getByLabel("导出正文来源", { exact: true })
      .selectOption("edition");
    const versions = page.getByLabel("历史成书版本", { exact: true });
    await expect(versions.locator(`option[value="${oldEdition}"]`)).toHaveCount(
      1,
    );
    await versions.selectOption(oldEdition);
    await expect(
      page.getByRole("region", { name: "导出内容预览", exact: true }),
    ).toHaveCount(0);
    await page
      .getByRole("button", { name: "检查导出内容", exact: true })
      .click();
    await expect(
      page.getByLabel("历史书籍信息", { exact: true }),
    ).toContainText("原版作者");
    const image = page.getByRole("img", {
      name: "本次导出的已采用封面",
      exact: true,
    });
    await expect
      .poll(() => image.evaluate((e) => e.complete && e.naturalWidth === 3))
      .toBe(true);
    expect(await image.getAttribute("src")).toBe(
      `/api/novels/${book}/export/editions/${oldEdition}/cover`,
    );
    if (await consent.count()) await consent.check();
    const oldDownload = page.waitForEvent("download");
    await page
      .getByRole("button", { name: "下载整合 TXT", exact: true })
      .click();
    const oldFile = await oldDownload;
    const text = await fs.readFile(await oldFile.path(), "utf8");
    expect(text).toContain("原版作者");
    expect(text).not.toContain("药铺的灯熄灭了");
    const bad = await page.request.post(
      `${origin}/api/novels/${book}/export/preview`,
      {
        headers: { Origin: origin },
        data: {
          source: "edition",
          edition_id: oldEdition,
          author: "篡改旧版署名",
        },
      },
    );
    expect(bad.status()).toBe(400);
    if (process.env.STUDIO_NOVEL_EDITION_SCREENSHOT)
      await page.screenshot({
        path: process.env.STUDIO_NOVEL_EDITION_SCREENSHOT,
        fullPage: true,
      });
    expect(errors).toEqual([]);
    console.log(
      "Real Vue/HTTP: explicitly saved published edition, downloaded Markdown, reloaded historical catalog, original archived cover and pre-revision prose preserved; no model call.",
    );
  } finally {
    await browser.close();
  }
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
