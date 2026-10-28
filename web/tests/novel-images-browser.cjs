const { chromium, expect } = require("@playwright/test");
(async () => {
  const [origin, title, book, imageTask, visualTask] = process.argv.slice(2);
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
    async function enter() {
      await page
        .getByRole("button", {
          name: `${title} · 继续完善世界与故事`,
          exact: true,
        })
        .click();
      await page
        .getByRole("button", { name: "封面与场景图", exact: true })
        .click();
      await expect(
        page.getByRole("heading", { name: "封面与场景图", exact: true }),
      ).toBeVisible();
    }
    await enter();
    await page.getByLabel("方案名称", { exact: true }).fill("雨夜钟楼");
    await page
      .getByLabel("出场角色（最多12位）", { exact: true })
      .selectOption("lin-1");
    const source = page.getByLabel("参考资料（最多12项）", { exact: true });
    await source.selectOption({ label: "钟楼场景资料" });
    await page
      .getByRole("button", { name: "进入图片方案", exact: true })
      .click();
    const panel = page.locator(".image-generator");
    await expect(
      panel.getByRole("heading", { name: "AI 封面与场景图", exact: true }),
    ).toBeVisible();
    await panel.getByLabel(/最近100次.*生成/).selectOption(imageTask);
    const image = panel.getByRole("img", {
      name: "AI生成的小说图片候选",
      exact: true,
    });
    await expect(image).toBeVisible();
    expect(
      await image.evaluate((e) => e.complete && e.naturalWidth === 4),
    ).toBe(true);
    const pixels = await (
      await page.request.get(
        new URL(await image.getAttribute("src"), origin).href,
      )
    ).body();
    await panel
      .getByRole("button", { name: "以此图继续修改", exact: true })
      .click();
    await expect(
      page.getByRole("button", { name: "返回小说工作台", exact: true }),
    ).toBeDisabled();
    await panel
      .getByRole("button", { name: "让助手完善视觉要求", exact: true })
      .click();
    const assist = panel.getByRole("region", {
      name: "视觉方案协作",
      exact: true,
    });
    await assist
      .getByRole("button", {
        name: `查看任务 ${visualTask.slice(0, 6)}`,
        exact: true,
      })
      .click();
    await assist
      .getByRole("button", { name: "查看完善建议", exact: true })
      .click();
    await expect(
      assist.getByText("是否保留远处人影？", { exact: true }),
    ).toBeVisible();
    await assist
      .getByLabel("可编辑的绘图要求", { exact: true })
      .fill("雨夜钟楼封面，仅增加石桥积水，保留书名留白。");
    await assist
      .getByRole("button", { name: "采用为本轮绘图要求", exact: true })
      .click();
    await panel.getByLabel(/本次费用预留/).fill("0.001");
    const saved = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        r.url().endsWith(`/novels/${book}/images/generate`),
    );
    await panel
      .getByRole("button", { name: "保存小说图片生成任务", exact: true })
      .click();
    const response = await saved;
    expect(response.status()).toBe(201);
    const task = await response.json();
    expect(task.frozen.image.novel.book_id).toBe(book);
    expect(task.frozen.image.novel.kind).toBe("cover");
    expect(task.frozen.image.visual_generation_id).toBe(visualTask);
    await page.reload();
    await enter();
    await page
      .getByRole("button", { name: "进入图片方案", exact: true })
      .click();
    await panel.getByLabel(/最近100次.*生成/).selectOption(task.id);
    await expect(panel.getByRole("status")).toContainText("已保存，等待开始");
    await expect(page.locator(".gallery")).toContainText("雨夜钟楼");
    await page.getByLabel("用途", { exact: true }).selectOption("scene");
    await page.getByLabel("方案名称", { exact: true }).fill("手绘钟楼参考");
    await page.locator('input[type="file"]').setInputFiles({
      name: "tower.png",
      mimeType: "image/png",
      buffer: pixels,
    });
    await page
      .getByLabel("画面说明", { exact: true })
      .fill("测试作者提供的钟楼构图");
    await page.getByLabel("来源", { exact: true }).fill("作者自绘");
    await page.getByLabel("使用许可", { exact: true }).fill("作者自有素材");
    await expect(
      page.getByRole("button", { name: "返回小说工作台", exact: true }),
    ).toBeDisabled();
    await page.getByRole("button", { name: "保存参考图", exact: true }).click();
    const uploaded = page.locator(".gallery article").filter({
      has: page.getByRole("heading", {
        name: "场景 · 手绘钟楼参考",
        exact: true,
      }),
    });
    await expect(uploaded).toContainText("待采用");
    await uploaded
      .getByRole("button", { name: "准备采用这张图片", exact: true })
      .click();
    await uploaded
      .getByRole("button", { name: "确认采用图片", exact: true })
      .click();
    await expect(uploaded).toContainText("已采用参考");
    expect(errors).toEqual([]);
    if (process.env.STUDIO_NOVEL_IMAGES_SCREENSHOT)
      await page.screenshot({
        path: process.env.STUDIO_NOVEL_IMAGES_SCREENSHOT,
        fullPage: true,
      });
    process.stdout.write(
      "Novel images: real workspace entry, original pixels, source selection, visual feedback, save/reload, upload and explicit adoption; no paid start.\n",
    );
  } finally {
    await browser.close();
  }
})().catch((e) => {
  console.error(e);
  process.exitCode = 1;
});
