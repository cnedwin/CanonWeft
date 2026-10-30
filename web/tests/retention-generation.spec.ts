import { test, expect } from "@playwright/test";
import { loginAsOwner } from "./owner-session";
import { openIPSection } from "./ip-edit-helper";

test("任务材料到期保留历史并清除旧详情，作者未保存输入不丢失", async ({
  page,
}) => {
  // Explicit UI response fixture. Independent disposable-MySQL integration
  // verifies source/reference/evidence binding and refuses forged projections.
  const id = "d".repeat(32);
  const liveID = "e".repeat(32);
  const frozen = {
    role: "writer",
    config: { name: "历史写作配置", model: "test", revision: 1 },
    price: { currency: "USD" },
    estimate_micros: 10,
  };
  const task = {
    id,
    state: "SUCCEEDED",
    paused: false,
    cancelled: false,
    frozen,
    output: { text: "即将到期的候选原文", input_tokens: 1, output_tokens: 1 },
  };
  let expired = false;
  await page.route("**/generation-options", async (route) => {
    await route.fulfill({
      json: {
        items: [
          {
            role: "writer",
            name: "测试写作配置",
            model: "test",
            price: { id: "f".repeat(32), currency: "USD" },
          },
        ],
      },
    });
  });
  await page.route(
    /\/api\/projects\/[^/]+\/generations\?branch=/,
    async (route) => {
      await route.fulfill({
        json: {
          runtime_ready: true,
          items: [
            expired
              ? {
                  id,
                  state: "SUCCEEDED",
                  frozen: {},
                  retention: {
                    state: "EXPIRED",
                    expired_at: "2026-10-25T00:00:00Z",
                  },
                }
              : task,
            { ...task, id: liveID, output: undefined, state: "PREPARED" },
          ],
        },
      });
    },
  );
  await page.route(`**/generations/${id}`, async (route) => {
    if (expired)
      await route.fulfill({
        status: 410,
        json: { error: "运行材料已到保留期限" },
      });
    else await route.fulfill({ json: task });
  });
  await loginAsOwner(page);
  await page
    .getByLabel("项目名称", { exact: true })
    .fill(`过期任务展示 ${Date.now()}`);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByLabel("分支名称", { exact: true }).fill("正史");
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  await openIPSection(page, "知识与任务");
  await expect(page.locator(".generation-task")).toHaveCount(2);
  await page.getByRole("button", { name: "查看任务 dddddd" }).click();
  await expect(page.locator(".generation-detail")).toContainText(
    "即将到期的候选原文",
  );
  await page.getByLabel("希望 AI 协助的内容").fill("我尚未保存的创作要求");
  expired = true;
  const history = page
    .locator(".generation-task")
    .filter({ hasText: "已过期的任务" });
  await expect(history).toContainText("已记录费用和未知占用仍保留");
  await expect(history.getByRole("button")).toHaveCount(0);
  await expect(page.locator(".generation-detail")).toHaveCount(0);
  await expect(page.locator(".generation-task")).toHaveCount(2);
  await expect(page.getByLabel("希望 AI 协助的内容")).toHaveValue(
    "我尚未保存的创作要求",
  );
  await page.screenshot({
    path: "../.runtime/retention-generation-history.png",
    fullPage: true,
  });
});
