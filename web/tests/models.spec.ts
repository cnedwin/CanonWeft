import { loginAsOwner } from "./owner-session";
import { test, expect } from "@playwright/test";

test("五家模型预置、Key 不回显、用途绑定与停用持久保存", async ({ page }) => {
  await loginAsOwner(page);
  await page.getByRole("button", { name: "模型与用途设置" }).click();
  await expect(page.getByRole("button", { name: /^添加 / })).toHaveCount(5);
  await page
    .getByRole("button", { name: "添加 DeepSeek", exact: true })
    .click();
  const name = `写作配置 ${Date.now()}`;
  await page.getByLabel("配置名称", { exact: true }).fill(name);
  await page
    .getByLabel("API Key", { exact: true })
    .fill("test-only-browser-key-not-real");
  await page.getByRole("button", { name: "保存模型配置", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("未发起模型调用");
  await expect(page.getByLabel("API Key", { exact: true })).toHaveValue("");
  await page
    .getByLabel("使用的模型", { exact: true })
    .selectOption({ label: `${name} · deepseek-flash` });
  await page.getByRole("button", { name: "保存用途绑定" }).click();
  await expect(page.getByRole("status")).toContainText("用途绑定已保存");
  await page.reload();
  await page.getByRole("button", { name: "模型与用途设置" }).click();
  await page
    .getByRole("button", { name: `${name} · 启用 · 已保存 Key`, exact: true })
    .click();
  await expect(page.getByLabel("API Key", { exact: true })).toHaveValue("");
  await page.getByLabel("启用配置", { exact: true }).uncheck();
  await page.getByRole("button", { name: "保存模型配置", exact: true }).click();
  await expect(
    page.getByRole("button", {
      name: `${name} · 停用 · 已保存 Key`,
      exact: true,
    }),
  ).toBeVisible();
  await expect(page.getByLabel("允许预算范围内的付费调用")).not.toBeChecked();
  await page.getByLabel("累计预算总额").fill("10.25");
  await page.getByLabel("单次请求上限").fill("0.50");
  await page.getByRole("button", { name: "保存预算设置" }).click();
  await expect(
    page.getByText("预算已保存，付费调用保持关闭。", { exact: true }),
  ).toBeVisible();
});
