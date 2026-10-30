import { loginAsOwner } from "./owner-session";
import { test, expect } from "@playwright/test";
import { openIPTool } from "./ip-edit-helper";

test("实时预览界面：刷新后可读暂存内容，过期后清除；受控UI响应不调用模型", async ({
  page,
}) => {
  await loginAsOwner(page);
  const projectName = `实时预览 ${Date.now()}`;
  await page.getByLabel("项目名称", { exact: true }).fill(projectName);
  const created = page.waitForResponse(
    (r) => r.url().endsWith("/api/projects") && r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  const project = await (await created).json();
  const id = "111111aaaaaaaaaaaaaaaaaaaaaaaaaa";
  const otherID = "222222bbbbbbbbbbbbbbbbbbbbbbbbbb";
  const first = {
    id,
    state: "RUNNING",
    paused: false,
    cancelled: false,
    frozen: {
      stream: true,
      role: "writer",
      context_id: "",
      base_head: "",
      estimate_micros: 100,
      config: { name: "本地界面测试", model: "fixture", revision: 1 },
      price: { currency: "USD" },
    },
    preview: {
      text: "第一段：林舟推开门。",
      sequence: 1,
      hash: "a".repeat(64),
      provisional: true,
    },
  };
  const second = {
    ...first,
    id: otherID,
    preview: { ...first.preview, text: "第二个任务的私人预览" },
  };
  const root = `/api/projects/${project.id}/generations`;
  let denied = false;
  let delayFirst = false;
  let release!: () => void;
  const delayed = new Promise<void>((r) => {
    release = r;
  });
  let arrived!: () => void;
  const seen = new Promise<void>((r) => {
    arrived = r;
  });
  await page.route(`**${root}?*`, (route) =>
    route.fulfill({ json: { items: [first, second], runtime_ready: true } }),
  );
  await page.route(`**${root}/${id}`, async (route) => {
    if (delayFirst) {
      arrived();
      await delayed;
    }
    await route.fulfill({ json: first });
  });
  await page.route(`**${root}/${otherID}`, (route) =>
    route.fulfill(
      denied
        ? { status: 409, json: { error: "设定已变化，请重新检查" } }
        : { json: second },
    ),
  );
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByLabel("分支名称", { exact: true }).fill("正史");
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  await openIPTool(page, "AI候选");
  const panel = page.locator(".generation-panel");
  await panel.getByRole("button", { name: "查看任务 111111" }).click();
  await expect(panel.getByLabel("未确认的生成预览")).toContainText(
    "第一段：林舟推开门。",
  );
  await expect(panel.getByLabel("未确认的生成预览")).toContainText(
    "不能采用或发布",
  );
  await page.reload();
  await page.getByRole("button", { name: projectName, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page
    .getByRole("navigation", { name: "设定分支" })
    .getByRole("button", { name: "正史", exact: true })
    .click();
  await openIPTool(page, "AI候选");
  await panel.getByRole("button", { name: "查看任务 111111" }).click();
  await expect(panel.getByLabel("未确认的生成预览")).toContainText(
    "第一段：林舟推开门。",
  );
  delayFirst = true;
  await seen;
  await panel.getByRole("button", { name: "查看任务 222222" }).click();
  await expect(panel.getByLabel("未确认的生成预览")).toContainText(
    "第二个任务的私人预览",
  );
  release();
  await page.waitForTimeout(200);
  await expect(panel.getByLabel("未确认的生成预览")).toContainText(
    "第二个任务的私人预览",
  );
  await page.screenshot({
    path: "../.runtime/generation-stream.png",
    fullPage: true,
  });
  denied = true;
  await expect(panel.getByLabel("未确认的生成预览")).toHaveCount(0);
  await expect(panel.getByRole("alert")).toContainText("设定已变化");
});
