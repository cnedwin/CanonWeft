import { test, expect } from "@playwright/test";

test("作者登录、创建中文项目、切换并刷新后保留，退出后不能访问", async ({
  page,
}) => {
  await page.goto("/");
  await page.getByLabel("用户名", { exact: true }).fill("owner");
  await page
    .getByLabel("密码", { exact: true })
    .fill("test-owner-password-123");
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "我的创作项目" }),
  ).toBeVisible();
  const name = `星河档案 ${Date.now()}`;
  await page.getByLabel("项目名称", { exact: true }).fill(name);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await expect(page.getByRole("button", { name, exact: true })).toBeVisible();
  await page.getByRole("button", { name, exact: true }).click();
  await page.getByLabel("修改项目名称").fill(name + "·修订");
  await page.getByRole("button", { name: "保存名称" }).click();
  await expect(
    page.getByRole("button", { name: name + "·修订", exact: true }),
  ).toBeVisible();
  await page.reload();
  await expect(
    page.getByRole("button", { name: name + "·修订", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "退出登录" }).click();
  await expect(
    page.getByRole("button", { name: "登录", exact: true }),
  ).toBeVisible();
});
