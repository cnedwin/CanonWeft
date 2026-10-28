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
    async function enter() {
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
        .getByRole("button", { name: /起名验收：朴素的边城守军.*2人/ })
        .click();
    }
    await enter();
    const panel = page.getByRole("region", {
      name: "角色起名工作台",
      exact: true,
    });
    const kept = panel.getByRole("article", {
      name: "名字候选 杨守砚",
      exact: true,
    });
    await expect(kept.getByLabel("名字", { exact: true })).toBeDisabled();
    await expect(kept.getByLabel("锁定", { exact: true })).toBeChecked();
    const chosen = panel.getByRole("article", {
      name: "名字候选 顾闻钟",
      exact: true,
    });
    await chosen.getByLabel("读音／音译", { exact: true }).fill("Gù Wénzhōng");
    await chosen.getByLabel("收藏", { exact: true }).check();
    await chosen.getByText("姓、名、字、号及原文", { exact: true }).click();
    await chosen.getByLabel("候选姓氏", { exact: true }).fill("顾");
    await chosen.getByLabel("候选名字", { exact: true }).fill("闻钟");
    await chosen.getByLabel("候选字", { exact: true }).fill("守砚");
    await panel.getByLabel("筛选名字候选", { exact: true }).fill("顾闻钟");
    await expect(panel.getByRole("article")).toHaveCount(1);
    await chosen
      .getByRole("button", { name: "用于选定角色", exact: true })
      .click();
    await page
      .getByRole("button", { name: "保存角色候选", exact: true })
      .click();
    await expect(page.getByText(/当前候选已保存 · 批次/)).toBeVisible();
    await page.reload();
    await enter();
    await expect(chosen.getByLabel("收藏", { exact: true })).toBeChecked();
    await expect(chosen.getByLabel("读音／音译", { exact: true })).toHaveValue(
      "Gù Wénzhōng",
    );
    await expect(
      panel
        .getByLabel("选名目标角色", { exact: true })
        .locator("option")
        .first(),
    ).toContainText("顾闻钟");
    await panel.getByText("浏览预置姓氏素材", { exact: true }).click();
    await panel
      .getByRole("button", { name: "查询姓氏素材", exact: true })
      .click();
    await expect(panel.getByText(/101条 ·/)).toBeVisible();
    await panel
      .getByRole("button", { name: "编辑名字素材库", exact: true })
      .click();
    const library = page.getByRole("dialog", {
      name: "编辑名字素材库",
      exact: true,
    });
    await expect(library.getByText(/共501条/)).toBeVisible();
    await library
      .getByRole("button", { name: "新增名字素材", exact: true })
      .click();
    await library.getByLabel("素材名字素材", { exact: true }).fill("听雨客");
    await library
      .getByLabel("素材名字类型", { exact: true })
      .selectOption("art_name");
    await library
      .getByLabel("素材文化与地域", { exact: true })
      .fill("架空江湖");
    await expect(
      library.getByRole("button", { name: "关闭素材库", exact: true }),
    ).toBeDisabled();
    await library
      .getByRole("button", { name: "保存名字素材", exact: true })
      .click();
    await expect(library.getByRole("status")).toContainText("名字素材已保存");
    await library
      .getByRole("button", { name: "关闭素材库", exact: true })
      .click();
    await page
      .getByRole("button", { name: "保存角色候选", exact: true })
      .click();
    await expect(page.getByText(/当前候选已保存 · 批次/)).toBeVisible();
    await page.reload();
    await enter();
    await chosen.getByText("姓、名、字、号及原文", { exact: true }).click();
    await expect(chosen.getByLabel("候选字", { exact: true })).toHaveValue(
      "守砚",
    );
    await panel.getByText("浏览预置姓氏素材", { exact: true }).click();
    await panel
      .getByRole("button", { name: "编辑名字素材库", exact: true })
      .click();
    await library
      .getByLabel("搜索库中名字素材", { exact: true })
      .fill("听雨客");
    await library
      .getByRole("button", { name: "听雨客 · 号 · zh", exact: true })
      .click();
    await expect(
      library.getByLabel("素材文化与地域", { exact: true }),
    ).toHaveValue("架空江湖");
    await library
      .getByRole("button", { name: "关闭素材库", exact: true })
      .click();
    if (process.env.STUDIO_NAMING_SCREENSHOT)
      await page.screenshot({
        path: process.env.STUDIO_NAMING_SCREENSHOT,
        fullPage: true,
      });
    await panel
      .getByRole("button", { name: "保存条件并让起名助手生成", exact: true })
      .click();
    const pending = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" && /\/naming\/assist$/.test(r.url()),
    );
    await page.getByRole("button", { name: /保存请求/ }).click();
    expect((await pending).status()).toBe(201);
    expect(errors).toEqual([]);
    console.log(
      "Naming UI: locked names protected, pronunciation/favorite saved across reload, filtered and applied to stable character draft, real surname sources listed, naming task prepared without paid call.",
    );
  } finally {
    await browser.close();
  }
})().catch((e) => {
  console.error(e);
  process.exitCode = 1;
});
