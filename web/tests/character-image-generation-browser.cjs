const { chromium, expect } = require("@playwright/test");
(async () => {
  const [origin, visualTask, imageTask] = process.argv.slice(2);
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
      await page.getByRole("button", { name: "AI三视图", exact: true }).click();
      await page
        .getByRole("button", { name: "打开设定库", exact: true })
        .click();
      await page.getByRole("button", { name: "正史", exact: true }).click();
      await page.getByRole("button", { name: /打开角色 .*lin-1/ }).click();
      await page
        .getByRole("navigation", { name: "角色详情分区" })
        .getByRole("button", { name: "图片与三视图", exact: true })
        .click();
    }
    await enter();
    const panel = page.locator(".image-generator");
    await expect(
      panel.getByRole("heading", { name: "AI 角色三视图", exact: true }),
    ).toBeVisible();
    await expect(
      panel.getByText("GPT Image 2.1", { exact: true }),
    ).toBeVisible();
    await panel.getByLabel("本角色最近100次生成").selectOption(imageTask);
    await expect(
      panel.getByRole("img", { name: "AI生成的角色三视图候选" }),
    ).toBeVisible();
    expect(
      await panel
        .getByRole("img", { name: "AI生成的角色三视图候选", exact: true })
        .evaluate((e) => e.complete && e.naturalWidth === 4),
    ).toBe(true);
    await expect(
      panel.getByRole("img", { name: "修改前原图", exact: true }),
    ).toBeVisible();
    await panel
      .getByRole("button", { name: "以此图继续修改", exact: true })
      .click();
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
      assist.getByText("银环是否采用？", { exact: true }),
    ).toBeVisible();
    await assist
      .getByLabel("可编辑的绘图要求", { exact: true })
      .fill("只把腰带改为深红，保留角色脸部和左耳银环");
    await assist
      .getByRole("button", { name: "采用为本轮绘图要求", exact: true })
      .click();
    await expect(
      panel.getByLabel("视觉要求 / 本轮修改意见", { exact: true }),
    ).toHaveValue("只把腰带改为深红，保留角色脸部和左耳银环");
    await panel.getByLabel("只修改框选区域", { exact: true }).check();
    const region = panel
      .locator("form")
      .getByRole("region", { name: "局部修改区域", exact: true });
    await expect(
      region.getByRole("button", { name: "添加中央选区", exact: true }),
    ).toBeEnabled();
    const bounds = await region.getByTestId("region-stage").boundingBox();
    await page.mouse.move(
      bounds.x + bounds.width * 0.25,
      bounds.y + bounds.height * 0.25,
    );
    await page.mouse.down();
    await page.mouse.move(
      bounds.x + bounds.width * 0.75,
      bounds.y + bounds.height * 0.75,
    );
    await page.mouse.up();
    await expect(region).toContainText("1 个选区");
    await region
      .getByLabel("必须保留的特征", { exact: true })
      .fill("保留面部和左耳银环，仅改腰带");
    await panel.getByLabel(/本次费用预留/).fill("0.001");
    const saved = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        r.url().endsWith("/character-images/generate"),
    );
    await panel
      .getByRole("button", { name: "保存三视图生成任务", exact: true })
      .click();
    const response = await saved;
    expect(response.status()).toBe(201);
    const task = await response.json();
    expect(task.frozen.image.references).toHaveLength(1);
    expect(task.frozen.image.visual_generation_id).toBe(visualTask);
    expect(task.frozen.config.model).toBe("test-image-protocol");
    expect(task.state).toBe("PREPARED");
    expect(task.frozen.image.region.source_id).toBe(
      task.frozen.image.references[0].id,
    );
    expect(task.frozen.image.region.areas).toHaveLength(1);
    expect(task.frozen.image.region.areas[0].width).toBeGreaterThan(4900);
    await expect(panel.getByRole("status")).toContainText("已保存，等待开始");
    await expect(
      panel.getByRole("region", { name: "局部修改区域", exact: true }),
    ).toContainText("保留面部和左耳银环，仅改腰带");
    await page.reload();
    await enter();
    await expect(panel.getByLabel("本角色最近100次生成")).toHaveValue(task.id);
    await expect(
      panel.getByRole("region", { name: "局部修改区域", exact: true }),
    ).toContainText("保留面部和左耳银环，仅改腰带");
    await expect(panel.getByRole("status")).toContainText("已保存，等待开始");
    expect(errors).toEqual([]);
    if (process.env.STUDIO_CHARACTER_IMAGE_SCREENSHOT)
      await panel.screenshot({
        path: process.env.STUDIO_CHARACTER_IMAGE_SCREENSHOT,
      });
    process.stdout.write(
      "Real Vue/HTTP/MySQL image browser: candidate preview, pinned-reference feedback, save and reload; no paid start.\n",
    );
  } finally {
    await browser.close();
  }
})().catch((e) => {
  console.error(e);
  process.exitCode = 1;
});
