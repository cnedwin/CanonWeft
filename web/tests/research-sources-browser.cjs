const { chromium, expect } = require("@playwright/test");
(async () => {
  const [origin, title, source, project, branch] = process.argv.slice(2);
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
        .getByRole("button", { name: "题材研究与资料采集", exact: true })
        .click();
      await page.getByText("按题材搜索并采集公开资料", { exact: true }).click();
    }
    await enter();
    const root = `${origin}/api/projects/${project}/branches/${branch}/research-sources`;
    // Only public network responses are substituted here. Database collection,
    // parser, permissions and idempotency use the real service in the Go test.
    await page.route(`${root}/search`, async (route) => {
      const body = route.request().postDataJSON();
      if (body.language !== "zh" || body.query !== "宋代 市舶司")
        throw new Error("search input lost");
      await route.fulfill({
        json: {
          items: [
            {
              page_id: 42,
              title: "市舶司",
              snippet: "港口制度",
              url: "https://zh.wikipedia.org/?curid=42",
            },
          ],
        },
      });
    });
    await page.route(`${root}/collect`, async (route) => {
      const body = route.request().postDataJSON();
      if (body.page_id !== 42 || !body.operation_id)
        throw new Error("collect identity lost");
      await route.fulfill({ status: 201, json: { id: source } });
    });
    await page.getByLabel("研究关键词", { exact: true }).fill("宋代 市舶司");
    await page
      .getByRole("button", { name: "搜索公开资料", exact: true })
      .click();
    await page
      .getByRole("button", { name: "采集并编辑：市舶司", exact: true })
      .click();
    const text = page.getByLabel("导入内容 · 可修改", { exact: true });
    await expect(text).toHaveValue(/oldid=71/);
    await expect(text).toHaveValue(/CC BY-SA/);
    const saved = page.waitForResponse(
      (r) =>
        r.url().endsWith(`/imports/${source}/draft`) &&
        r.request().method() === "PUT" &&
        r.status() === 200,
    );
    await text.fill(
      (await text.inputValue()) + "\n作者备注：先核实具体年代和港口。",
    );
    await saved;
    await expect(
      page.getByText("导入草稿已自动暂存", { exact: true }),
    ).toBeVisible();
    await page.reload();
    await enter();
    await expect(text).toHaveValue(/作者备注：先核实具体年代和港口。/);
    await page.getByText("已采集资料（最近100项）", { exact: true }).click();
    await expect(
      page.getByRole("button", { name: "编辑采集稿：市舶司", exact: true }),
    ).toBeVisible();
    if (process.env.STUDIO_RESEARCH_SCREENSHOT)
      await page.screenshot({
        path: process.env.STUDIO_RESEARCH_SCREENSHOT,
        fullPage: true,
      });
    if (errors.length) throw new Error(errors.join("\n"));
    console.log(
      "Research UI: search and selection, saved source version/license, private edits across reload and persisted history; public source responses substituted, no paid model calls.",
    );
  } finally {
    await browser.close();
  }
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
