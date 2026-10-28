import { loginAsOwner } from "./owner-session";
import { test, expect } from "@playwright/test";

test("删除项目须完整名称确认，立即移出书架且刷新后不能访问", async ({
  page,
}) => {
  await loginAsOwner(page);
  const name = `仅测试删除 ${Date.now()}`;
  await page.getByLabel("项目名称", { exact: true }).fill(name);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await expect(page.getByRole("button", { name, exact: true })).toBeVisible();
  await page.getByRole("button", { name: "删除项目", exact: true }).click();
  await expect(
    page.getByText(
      "立即撤销所有成员、后台任务和文件的访问；历史与文件仍保留，尚未执行物理清理。",
      { exact: true },
    ),
  ).toBeVisible();
  const confirm = page.getByRole("button", {
    name: "确认删除并撤销访问",
    exact: true,
  });
  await expect(confirm).toBeDisabled();
  await page.getByLabel("输入完整项目名称确认删除").fill("错误名称");
  await expect(confirm).toBeDisabled();
  await page.getByLabel("输入完整项目名称确认删除").fill(name);
  await expect(confirm).toBeEnabled();
  await confirm.click();
  await expect(page.getByRole("button", { name, exact: true })).toHaveCount(0);
  await expect(
    page.getByText("项目访问已撤销，历史与文件保留待后续清理。", {
      exact: true,
    }),
  ).toBeVisible();
  await page.reload();
  await expect(page.getByRole("button", { name, exact: true })).toHaveCount(0);
});
