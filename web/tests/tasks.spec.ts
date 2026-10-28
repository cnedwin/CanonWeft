import { loginAsOwner } from "./owner-session";
import { saveReasonedCandidate } from "./ip-edit-helper";
import { test, expect } from "@playwright/test";
test("持久候选检查支持暂停恢复取消且不自动采用", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await loginAsOwner(page);
  const name = `任务 ${Date.now()}`;
  await page.getByLabel("项目名称", { exact: true }).fill(name);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByLabel("分支名称", { exact: true }).fill("正史");
  await page.getByRole("button", { name: "创建分支", exact: true }).click();
  await page.getByRole("button", { name: "新增角色或实体" }).click();
  await page.getByLabel("实体名称", { exact: true }).fill("沈晴");
  await saveReasonedCandidate(page);
  await page
    .getByLabel("待检查候选", { exact: true })
    .selectOption({ index: 1 });
  await page.getByRole("button", { name: "启动持久检查" }).click();
  const card = page.getByTestId("task-card").first();
  await expect(
    card.getByRole("heading", { name: "等待确认采用", exact: true }),
  ).toBeVisible();
  await card.getByRole("button", { name: "暂停任务" }).click();
  await expect(
    card.getByRole("heading", { name: "已暂停", exact: true }),
  ).toBeVisible();
  await page.reload();
  await page.getByRole("button", { name, exact: true }).click();
  await page.getByRole("button", { name: "打开设定库" }).click();
  await page.getByRole("button", { name: "正史", exact: true }).click();
  await expect(
    card.getByRole("heading", { name: "已暂停", exact: true }),
  ).toBeVisible();
  await card.getByRole("button", { name: "恢复任务" }).click();
  await expect(
    card.getByRole("heading", { name: "等待确认采用", exact: true }),
  ).toBeVisible();
  await card.getByRole("button", { name: "取消任务" }).click();
  await expect(
    card.getByRole("heading", { name: "已取消", exact: true }),
  ).toBeVisible();
  await expect(page.getByText("正式版本尚无实体")).toBeVisible();
  expect(errors).toEqual([]);
});
