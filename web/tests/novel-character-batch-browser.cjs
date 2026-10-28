const { chromium, expect } = require("@playwright/test");
(async () => {
  const [origin, title, design, run] = process.argv.slice(2);
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
        .getByRole("button", { name: /分段群像测试：十一位城中人.*11人/ })
        .click();
      await page
        .getByRole("button", { name: "分段生成 / 恢复任务", exact: true })
        .click();
      await page
        .getByRole("button", {
          name: `已完成 · ${run.slice(0, 8)}`,
          exact: true,
        })
        .click();
    };
    await open();
    await expect(
      page.getByRole("heading", {
        name: "已完成 · 已完成 11 / 11 人",
        exact: true,
      }),
    ).toBeVisible();
    await expect(page.getByText(/第 2 段 · 已完成 · 尝试 2/)).toBeVisible();
    await page.reload();
    await open();
    await page
      .getByRole("button", { name: "预览整批角色候选", exact: true })
      .click();
    const preview = page.getByRole("region", {
      name: "角色修改建议",
      exact: true,
    });
    await expect(
      preview.getByText(
        "正在预览助手建议；放入编辑区并保存后，仍须选择加入本书设定草稿。",
        { exact: true },
      ),
    ).toBeVisible();
    await expect(preview.getByRole("article")).toHaveCount(11);
    await expect(
      preview.getByRole("article").filter({ hasText: "分段人物11" }),
    ).toHaveCount(1);
    if (process.env.STUDIO_CHARACTER_BATCH_SCREENSHOT)
      await preview.screenshot({
        path: process.env.STUDIO_CHARACTER_BATCH_SCREENSHOT,
      });
    await page
      .getByRole("button", { name: "将建议放入编辑区", exact: true })
      .click();
    await page
      .getByRole("button", { name: "保存角色候选", exact: true })
      .click();
    await expect(page.getByText(/当前候选已保存 · 批次/)).toBeVisible();
    await page
      .getByRole("button", { name: "将选中角色加入本书设定草稿", exact: true })
      .click();
    await expect(
      page
        .getByRole("status")
        .filter({ hasText: "选中角色已加入本书设定草稿" }),
    ).toBeVisible();
    await page.reload();
    await open();
    await expect(page.getByText(/历史结果仍可查看/)).toBeVisible();
    await expect(
      page.getByRole("button", { name: "预览整批角色候选", exact: true }),
    ).toHaveCount(0);
    await page.getByLabel("本次整批费用上限", { exact: true }).fill("1");
    const prepared = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        new RegExp(`/character-designs/${design}/batches$`).test(r.url()),
    );
    await page
      .getByRole("button", { name: "保存分段任务（不调用模型）", exact: true })
      .click();
    const response = await prepared;
    expect(response.status()).toBe(201);
    const task = await response.json();
    expect(task.state).toBe("PREPARED");
    expect(task.steps).toHaveLength(3);
    expect(errors).toEqual([]);
    process.stdout.write(
      "Character batch browser passed: three-pass history/retry, reload, 11-card preview, save/adopt and stale-history protection; new preparation makes no provider call.\n",
    );
  } finally {
    await browser.close();
  }
})().catch((e) => {
  console.error(e);
  process.exitCode = 1;
});
