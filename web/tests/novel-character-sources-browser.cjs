const { chromium, expect } = require("@playwright/test");
(async () => {
  const [origin, title] = process.argv.slice(2);
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
    const open = async () => {
      await page
        .getByRole("button", {
          name: `${title} · 继续完善世界与故事`,
          exact: true,
        })
        .click();
      await page
        .getByRole("button", { name: "批量角色与一句话完善", exact: true })
        .click();
      await page
        .getByRole("button", { name: /根据附件塑造两名人物.*2人/ })
        .click();
      await expect(
        page.getByRole("heading", { name: "编辑与选择候选", exact: true }),
      ).toBeVisible();
    };
    await open();
    const selected = () =>
      page.getByRole("group", { name: "角色创作参考资料", exact: true }).last();
    await expect(
      selected().getByRole("checkbox", { name: /角色专业资料/ }),
    ).toBeChecked();
    await selected()
      .getByRole("button", { name: "上传、编辑与采用本书资料", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: "本书创作资料", exact: true }),
    ).toBeVisible();
    const original = "人物附件：码头学徒怕水，但熟悉潮汐。";
    await expect(page.getByLabel("上传资料文件")).toBeEnabled();
    const uploaded = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" && r.url().endsWith("/imports/jobs"),
    );
    await page.getByLabel("上传资料文件").setInputFiles({
      name: "角色附件.md",
      mimeType: "text/markdown",
      buffer: Buffer.from(original),
    });
    const uploadResult = await uploaded;
    const uploadBody = await uploadResult.json();
    expect(uploadResult.status(), JSON.stringify(uploadBody)).toBe(202);
    await expect(page.getByLabel("导入内容 · 可修改")).toHaveValue(original);
    await page
      .getByLabel("导入内容 · 可修改")
      .fill(original + "作者补充：只作为创作参考。");
    await expect(page.getByTestId("import-draft-status")).toHaveText(
      "导入草稿已自动暂存",
    );
    await page
      .getByRole("button", { name: "保存文件内容候选", exact: true })
      .click();
    await expect(
      page.getByText("文件内容已保存为候选，请在下方检查、确认采用并发布。", {
        exact: true,
      }),
    ).toBeVisible();
    await page
      .locator(".candidate-card")
      .filter({ hasText: "待检查" })
      .getByRole("button", { name: "检查联合候选", exact: true })
      .click();
    await expect(page.getByTestId("release-preview")).toContainText(
      original + "作者补充：只作为创作参考。",
    );
    await page
      .getByRole("button", { name: "确认采用整组内容", exact: true })
      .click();
    await page
      .getByRole("button", { name: "发布整组内容", exact: true })
      .click();
    await page
      .getByRole("button", { name: "返回角色创作", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: "编辑与选择候选", exact: true }),
    ).toBeVisible();
    await selected()
      .getByRole("checkbox", { name: /角色附件/ })
      .check();
    await page
      .getByRole("button", { name: "保存角色候选", exact: true })
      .click();
    await expect(page.getByText(/当前候选已保存 · 批次/)).toBeVisible();
    await page.reload();
    await open();
    await expect(
      selected().getByRole("checkbox", { name: /角色附件/ }),
    ).toBeChecked();
    await selected()
      .getByRole("checkbox", { name: /角色专业资料/ })
      .uncheck();
    await page
      .getByRole("button", { name: "保存角色候选", exact: true })
      .click();
    await expect(page.getByText(/当前候选已保存 · 批次/)).toBeVisible();
    await page.reload();
    await open();
    await expect(
      selected().getByRole("checkbox", { name: /角色专业资料/ }),
    ).not.toBeChecked();
    await selected()
      .getByRole("checkbox", { name: /角色专业资料/ })
      .check();
    await page
      .getByRole("button", { name: "保存角色候选", exact: true })
      .click();
    await expect(page.getByText(/当前候选已保存 · 批次/)).toBeVisible();
    if (process.env.STUDIO_CHARACTER_SOURCES_SCREENSHOT)
      await selected().screenshot({
        path: process.env.STUDIO_CHARACTER_SOURCES_SCREENSHOT,
      });
    expect(errors).toEqual([]);
    console.log(
      "Character sources browser passed: upload, edit, adopt, return to same candidate, source selection/removal and reload persistence; no model call.",
    );
  } finally {
    await browser.close();
  }
})().catch((e) => {
  console.error(e);
  process.exitCode = 1;
});
